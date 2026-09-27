package config

import (
	"testing"
	"time"
)

func TestEnvHelpers(t *testing.T) {
	t.Setenv("X_STR", "  hello ")
	t.Setenv("X_BOOL", "YES")
	t.Setenv("X_INT", "42")
	t.Setenv("X_DUR", "90")
	t.Setenv("X_DUR2", "2m")
	t.Setenv("X_LIST", "a, b ,,c")

	if got := Env("X_STR", "d"); got != "hello" {
		t.Fatalf("Env = %q", got)
	}
	if got := Env("X_MISSING", "d"); got != "d" {
		t.Fatalf("Env default = %q", got)
	}
	if !Bool("X_BOOL", false) {
		t.Fatal("Bool yes")
	}
	if Bool("X_MISSING", false) {
		t.Fatal("Bool default")
	}
	if got := Int("X_INT", 1); got != 42 {
		t.Fatalf("Int = %d", got)
	}
	if got := Duration("X_DUR", time.Second); got != 90*time.Second {
		t.Fatalf("Duration seconds = %v", got)
	}
	if got := Duration("X_DUR2", time.Second); got != 2*time.Minute {
		t.Fatalf("Duration parsed = %v", got)
	}
	if got := List("X_LIST", nil); len(got) != 3 || got[2] != "c" {
		t.Fatalf("List = %v", got)
	}
}

func TestLoadDefaultsAndOverrides(t *testing.T) {
	t.Setenv("PORT", "9999")
	t.Setenv("IAM_ISSUERS", "https://iam.byzantineapp.dev, https://iam.example.com")
	t.Setenv("BYZ_KAFKA_ENABLED", "false")
	t.Setenv("PUBLIC_URL", "https://api.example.com/kan/")

	s := Load("byz-kan", Service{Port: "8109", DBURL: "postgres://x"})
	if s.Name != "byz-kan" || s.Port != "9999" || s.Bind != DefaultBind {
		t.Fatalf("basic fields wrong: %+v", s)
	}
	if s.Addr() != "0.0.0.0:9999" {
		t.Fatalf("Addr = %q", s.Addr())
	}
	if s.PublicURL != "https://api.example.com/kan" {
		t.Fatalf("PublicURL not trimmed: %q", s.PublicURL)
	}
	if s.JWKSURL != DefaultJWKSURL {
		t.Fatalf("JWKSURL = %q", s.JWKSURL)
	}
	if len(s.Issuers) != 2 {
		t.Fatalf("Issuers = %v", s.Issuers)
	}
	if s.KafkaEnabled {
		t.Fatal("KafkaEnabled should honour env false")
	}
	if s.DBURL != "postgres://x" {
		t.Fatalf("DBURL = %q", s.DBURL)
	}
}

func TestLoadKafkaEnabledDefaultsTrue(t *testing.T) {
	t.Setenv("BYZ_KAFKA_ENABLED", "")
	s := Load("svc", Service{Port: "1"})
	if !s.KafkaEnabled {
		t.Fatal("KafkaEnabled should default true when env unset")
	}
	if len(s.KafkaBrokers) != 1 || s.KafkaBrokers[0] != DefaultKafka {
		t.Fatalf("KafkaBrokers = %v", s.KafkaBrokers)
	}
}
