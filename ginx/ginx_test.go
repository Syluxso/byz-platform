package ginx

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/Syluxso/byz-platform/httpx"
	"github.com/Syluxso/byz-platform/obs"
)

func init() { gin.SetMode(gin.TestMode) }

func fixture(t *testing.T) (*rsa.PrivateKey, jwt.Keyfunc) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key, func(*jwt.Token) (any, error) { return &key.PublicKey, nil }
}

func sign(t *testing.T, key *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	claims["exp"] = time.Now().Add(time.Hour).Unix()
	s, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGinChain(t *testing.T) {
	key, kf := fixture(t)
	v, err := newVerifier(kf)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	f := false
	logger, _ := obs.New("bb-api", obs.Options{Writer: &logs, SetDefault: &f})

	r := gin.New()
	r.Use(Recovery(logger), RequestID(), CORS(httpx.DefaultCORS), Metrics("bb-api"), Logger(logger))
	Health(r, func(context.Context) error { return nil })
	r.GET("/metrics", gin.WrapH(obs.Metrics()))
	api := r.Group("/api/v1", Auth(v))
	api.GET("/quotes/:id", RequireTenant(), func(c *gin.Context) {
		sc := MustScope(c)
		c.JSON(200, gin.H{"org": sc.OrgID, "tenant": sc.TenantID, "actor": sc.ActorID, "bearer": Bearer(c) != "", "id": c.Param("id")})
	})
	api.GET("/boom", func(*gin.Context) { panic("kaboom") })
	api.POST("/quotes", RequireTenant(), func(c *gin.Context) {
		var in struct {
			Name string `json:"name"`
		}
		if !Decode(c, &in) {
			return
		}
		c.JSON(201, gin.H{"name": in.Name})
	})

	// Preflight without auth.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("OPTIONS", "/api/v1/quotes/1", nil)
	req.Header.Set("Origin", "https://app.example")
	r.ServeHTTP(rr, req)
	if rr.Code != 204 || rr.Header().Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Fatalf("preflight: %d %v", rr.Code, rr.Header())
	}

	// Missing bearer: 401 problem from the shared iam middleware, chain aborted.
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/quotes/1", nil))
	if rr.Code != 401 || !strings.Contains(rr.Header().Get("WWW-Authenticate"), "Bearer") || !strings.Contains(rr.Body.String(), "iam.unauthorized") {
		t.Fatalf("401: %d %v %s", rr.Code, rr.Header(), rr.Body.String())
	}
	if rr.Header().Get(httpx.RequestIDHeader) == "" {
		t.Fatal("request id missing on 401")
	}

	// Valid token without tenant: 403 from the shared tenant policy.
	req = httptest.NewRequest("GET", "/api/v1/quotes/1", nil)
	req.Header.Set("Authorization", "Bearer "+sign(t, key, jwt.MapClaims{"sub": "u1", "organization_id": "o1"}))
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != 403 || !strings.Contains(rr.Body.String(), "tenant.missing_tenant") {
		t.Fatalf("403: %d %s", rr.Code, rr.Body.String())
	}

	// Valid token with tenant: handler sees scope and bearer.
	req = httptest.NewRequest("GET", "/api/v1/quotes/42", nil)
	req.Header.Set("Authorization", "Bearer "+sign(t, key, jwt.MapClaims{"sub": "u1", "organization_id": "o1", "tenant_id": "t1"}))
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if rr.Code != 200 || out["org"] != "o1" || out["tenant"] != "t1" || out["actor"] != "u1" || out["bearer"] != true || out["id"] != "42" {
		t.Fatalf("200: %d %v", rr.Code, out)
	}

	// Strict decode rejects unknown fields with a problem.
	req = httptest.NewRequest("POST", "/api/v1/quotes", strings.NewReader(`{"name":"a","x":1}`))
	req.Header.Set("Authorization", "Bearer "+sign(t, key, jwt.MapClaims{"sub": "u1", "organization_id": "o1", "tenant_id": "t1"}))
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != 400 || !strings.Contains(rr.Body.String(), "request.invalid_json") {
		t.Fatalf("decode: %d %s", rr.Code, rr.Body.String())
	}

	// Panic becomes a 500 problem, not Gin's HTML.
	req = httptest.NewRequest("GET", "/api/v1/boom", nil)
	req.Header.Set("Authorization", "Bearer "+sign(t, key, jwt.MapClaims{"sub": "u1", "organization_id": "o1"}))
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != 500 || !strings.Contains(rr.Body.String(), "internal.panic") {
		t.Fatalf("panic: %d %s", rr.Code, rr.Body.String())
	}

	// Health and metrics with the Gin route pattern as the label.
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest("GET", "/actuator/health", nil))
	if rr.Code != 200 {
		t.Fatalf("health: %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rr.Body.String(), `route="/api/v1/quotes/:id",service="bb-api",status="200"`) {
		t.Fatalf("metrics missing gin route label:\n%s", rr.Body.String())
	}
	if !strings.Contains(logs.String(), `"route":"/api/v1/quotes/:id"`) || !strings.Contains(logs.String(), `"org":"o1"`) {
		t.Fatalf("request log missing fields: %s", logs.String())
	}
}
