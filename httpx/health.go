package httpx

import (
	"context"
	"net/http"
	"time"
)

// ReadyFunc reports whether dependencies are reachable. Return nil when ready.
type ReadyFunc func(ctx context.Context) error

// Health registers the three health paths the fleet already exposes:
//
//	GET /healthz          liveness, always {"status":"UP"}
//	GET /readyz           readiness, runs ready; 503 with detail on failure
//	GET /actuator/health  Spring-compatible alias of /readyz for the admin UI
//
// ready may be nil, in which case /readyz behaves like /healthz.
func Health(mux *http.ServeMux, ready ReadyFunc) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "UP"})
	})
	rh := func(w http.ResponseWriter, r *http.Request) {
		if ready != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			if err := ready(ctx); err != nil {
				WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "DOWN", "detail": err.Error()})
				return
			}
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "UP"})
	}
	mux.HandleFunc("GET /readyz", rh)
	mux.HandleFunc("GET /actuator/health", rh)
}
