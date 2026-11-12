package crypto

import (
	"errors"
	"strings"
	"testing"
)

func ring(t *testing.T, spec string) *Keyring {
	t.Helper()
	k, err := ParseKeys(spec)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestRoundTrip(t *testing.T) {
	k := ring(t, NewKey())
	const plain = "sk_live_super_secret_key_do_not_log"
	v, err := k.Seal(plain, "row:1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(v, plain) || !strings.HasPrefix(v, "v1.k1.") {
		t.Fatalf("unexpected sealed value %q", v)
	}
	got, err := k.Open(v, "row:1")
	if err != nil || got != plain {
		t.Fatalf("open = %q, %v", got, err)
	}
}

func TestEmptyPlaintext(t *testing.T) {
	k := ring(t, NewKey())
	v, _ := k.Seal("", "a")
	got, err := k.Open(v, "a")
	if err != nil || got != "" {
		t.Fatalf("open = %q, %v", got, err)
	}
}

func TestFreshNonce(t *testing.T) {
	k := ring(t, NewKey())
	a, _ := k.Seal("same", "x")
	b, _ := k.Seal("same", "x")
	if a == b {
		t.Fatal("same plaintext sealed twice must differ")
	}
}

func TestAADBindsValueToOwner(t *testing.T) {
	k := ring(t, NewKey())
	v, _ := k.Seal("secret", "pay.provider_configs:A:secret_key")
	for _, other := range []string{"pay.provider_configs:B:secret_key", "pay.provider_configs:A:webhook_secret", ""} {
		if _, err := k.Open(v, other); !errors.Is(err, ErrOpen) {
			t.Fatalf("aad %q: want ErrOpen, got %v", other, err)
		}
	}
}

func TestTamper(t *testing.T) {
	k := ring(t, NewKey())
	v, _ := k.Seal("secret", "a")
	b := []byte(v)
	b[len(b)-3] ^= 0x01
	if _, err := k.Open(string(b), "a"); err == nil {
		t.Fatal("tampered value opened")
	}
}

func TestKeyIDCannotBeRelabelled(t *testing.T) {
	k1, k2 := NewKey(), NewKey()
	k := ring(t, "a:"+k1+",b:"+k2)
	v, _ := k.Seal("secret", "x") // sealed with a
	relabelled := strings.Replace(v, "v1.a.", "v1.b.", 1)
	if _, err := k.Open(relabelled, "x"); err == nil {
		t.Fatal("relabelled value opened")
	}
}

func TestRotation(t *testing.T) {
	oldKey, newKey := NewKey(), NewKey()
	before := ring(t, "k1:"+oldKey)
	v, _ := before.Seal("secret", "x")

	after := ring(t, "k2:"+newKey+",k1:"+oldKey)
	if !after.NeedsRotation(v) {
		t.Fatal("value under old key should need rotation")
	}
	got, err := after.Open(v, "x")
	if err != nil || got != "secret" {
		t.Fatalf("old value under rotated ring: %q, %v", got, err)
	}
	v2, _ := after.Seal(got, "x")
	if after.NeedsRotation(v2) || !strings.HasPrefix(v2, "v1.k2.") {
		t.Fatalf("resealed value %q should use k2", v2)
	}

	dropped := ring(t, "k2:"+newKey)
	if _, err := dropped.Open(v, "x"); !errors.Is(err, ErrOpen) {
		t.Fatalf("dropped key: want ErrOpen, got %v", err)
	}
}

func TestParseKeys(t *testing.T) {
	good := NewKey()
	bad := []string{
		"",
		"not-base64!!",
		"k1:" + good + "," + good, // second key without id
		"k1:" + good + ",k1:" + NewKey(),
		"k1:" + "c2hvcnQ=", // short
		"bad id:" + good,
	}
	for _, spec := range bad {
		if _, err := ParseKeys(spec); err == nil {
			t.Errorf("ParseKeys(%q) succeeded", spec)
		}
	}
	// URL-safe, unpadded keys are accepted.
	raw := strings.TrimRight(strings.NewReplacer("+", "-", "/", "_").Replace(good), "=")
	if _, err := ParseKeys(raw); err != nil {
		t.Fatalf("url-safe key: %v", err)
	}
}

func TestMalformedValues(t *testing.T) {
	k := ring(t, NewKey())
	for _, v := range []string{"", "plain", "v1.k1.", "v2.k1.abc", "v1.k9.abcd", "v1.k1.!!!"} {
		if _, err := k.Open(v, ""); !errors.Is(err, ErrOpen) {
			t.Errorf("Open(%q): want ErrOpen, got %v", v, err)
		}
	}
}
