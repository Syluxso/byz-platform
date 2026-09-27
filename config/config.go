// Package config reads the environment keys every Byz service shares.
//
// Frozen verbs: Env, Bool, Int, Duration, List, Load.
//
// Service-specific knobs stay in the service. This package only covers the
// keys that were copied into every config.go across the fleet.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Env returns the environment value for key, or def when unset or blank.
func Env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// Bool parses 1/true/yes/on and 0/false/no/off, case-insensitive.
// Anything else, including unset, returns def.
func Bool(key string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

// Int parses a base-10 integer, returning def when unset or malformed.
func Int(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// Duration parses a Go duration such as "30s" or "5m". A bare integer is
// treated as seconds. Unset or malformed returns def.
func Duration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

// List splits a comma-separated value, trimming blanks. Unset returns def.
func List(key string, def []string) []string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Service holds the settings every Byz service shares. Fields left blank in
// the defaults passed to Load fall back to the platform-wide defaults below.
type Service struct {
	Name      string
	Bind      string
	Port      string
	PublicURL string

	JWKSURL  string
	Issuers  []string
	Audience string

	KafkaBrokers []string
	KafkaEnabled bool

	DBURL string
}

// Platform-wide defaults used when a service does not override them.
const (
	DefaultBind    = "0.0.0.0"
	DefaultJWKSURL = "https://iam.byzantineapp.dev/.well-known/jwks.json"
	DefaultKafka   = "127.0.0.1:9092"
)

// Load reads the shared environment keys on top of the supplied defaults.
// Name is required and is used as the service label in logs and metrics.
//
// KafkaEnabled defaults to true when BYZ_KAFKA_ENABLED is unset, regardless of
// the zero value in defaults, because a false zero value is ambiguous.
func Load(name string, defaults Service) Service {
	s := defaults
	s.Name = name
	if s.Bind == "" {
		s.Bind = DefaultBind
	}
	if s.JWKSURL == "" {
		s.JWKSURL = DefaultJWKSURL
	}
	if len(s.KafkaBrokers) == 0 {
		s.KafkaBrokers = []string{DefaultKafka}
	}

	s.Bind = Env("BIND", s.Bind)
	s.Port = Env("PORT", s.Port)
	s.PublicURL = strings.TrimRight(Env("PUBLIC_URL", s.PublicURL), "/")
	s.JWKSURL = Env("IAM_JWKS_URL", s.JWKSURL)
	s.Issuers = List("IAM_ISSUERS", s.Issuers)
	s.Audience = Env("IAM_AUDIENCE", s.Audience)
	s.KafkaBrokers = List("KAFKA_BOOTSTRAP", s.KafkaBrokers)
	s.KafkaEnabled = Bool("BYZ_KAFKA_ENABLED", true)
	s.DBURL = Env("DB_URL", s.DBURL)
	return s
}

// Addr returns bind:port for http.Server.
func (s Service) Addr() string {
	return s.Bind + ":" + s.Port
}
