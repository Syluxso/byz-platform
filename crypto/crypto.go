// Package crypto encrypts small secrets a service stores on behalf of an
// org or tenant: provider API keys, webhook signing secrets, OAuth refresh
// tokens. AES-256-GCM, a random nonce per value, keys from the environment
// only. It knows nothing about payments or any other domain.
//
// Frozen verbs: ParseKeys, Seal, Open, NeedsRotation.
//
// Two rules make a stored value safe to keep in an ordinary column:
//
//   - Every value names the key that sealed it ("v1.<keyID>.<data>"), so the
//     key can be rotated: add a new primary key, keep the old one in the ring
//     until NeedsRotation reports nothing left, then drop it.
//   - Every value is bound to its owner through additional data (aad), for
//     example "pay.provider_configs:<id>:secret_key". A value copied onto
//     another row, org or column fails to open instead of decrypting there.
//
// Key spec (one env var, e.g. PAY_ENCRYPTION_KEYS):
//
//	k2:<base64 32 bytes>,k1:<base64 32 bytes>
//
// The first entry is the primary (used by Seal); the rest only open. A bare
// base64 key with no id is accepted as a single key with id "k1".
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// KeySize is the AES-256 key length in bytes.
const KeySize = 32

const formatVersion = "v1"

var keyIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// ErrOpen is returned for any value that cannot be opened: wrong key, wrong
// aad, tampering, or a malformed value. The cause is wrapped but deliberately
// not distinguished, so callers do not leak which check failed.
var ErrOpen = errors.New("crypto: cannot open value")

// Keyring holds one primary key that seals and any number of older keys that
// still open values sealed before a rotation. Safe for concurrent use.
type Keyring struct {
	primary string
	keys    map[string]cipher.AEAD
}

// NewKeyring builds a ring from raw 32-byte keys. primary must be in keys.
func NewKeyring(primary string, keys map[string][]byte) (*Keyring, error) {
	if len(keys) == 0 {
		return nil, errors.New("crypto: no keys")
	}
	if _, ok := keys[primary]; !ok {
		return nil, fmt.Errorf("crypto: primary key %q not in ring", primary)
	}
	ring := &Keyring{primary: primary, keys: make(map[string]cipher.AEAD, len(keys))}
	for id, k := range keys {
		if !keyIDRe.MatchString(id) {
			return nil, fmt.Errorf("crypto: invalid key id %q", id)
		}
		if len(k) != KeySize {
			return nil, fmt.Errorf("crypto: key %q must be %d bytes, got %d", id, KeySize, len(k))
		}
		block, err := aes.NewCipher(k)
		if err != nil {
			return nil, fmt.Errorf("crypto: key %q: %w", id, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("crypto: key %q: %w", id, err)
		}
		ring.keys[id] = aead
	}
	return ring, nil
}

// ParseKeys reads a key spec (see package doc). Keys may be standard or URL
// base64, padded or not.
func ParseKeys(spec string) (*Keyring, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, errors.New("crypto: empty key spec")
	}
	keys := map[string][]byte{}
	primary := ""
	for i, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, enc := "k1", part
		if j := strings.Index(part, ":"); j >= 0 {
			id, enc = strings.TrimSpace(part[:j]), strings.TrimSpace(part[j+1:])
		} else if i > 0 {
			return nil, errors.New("crypto: every key after the first needs an id (id:base64)")
		}
		if _, dup := keys[id]; dup {
			return nil, fmt.Errorf("crypto: duplicate key id %q", id)
		}
		raw, err := decodeKey(enc)
		if err != nil {
			return nil, fmt.Errorf("crypto: key %q: %w", id, err)
		}
		keys[id] = raw
		if primary == "" {
			primary = id
		}
	}
	return NewKeyring(primary, keys)
}

func decodeKey(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("not valid base64")
}

// Primary is the id of the key Seal uses.
func (k *Keyring) Primary() string { return k.primary }

// Seal encrypts plaintext with the primary key, bound to aad.
func (k *Keyring) Seal(plaintext, aad string) (string, error) {
	if k == nil {
		return "", errors.New("crypto: nil keyring")
	}
	aead := k.keys[k.primary]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("crypto: nonce: %w", err)
	}
	sealed := aead.Seal(nonce, nonce, []byte(plaintext), k.aad(k.primary, aad))
	return formatVersion + "." + k.primary + "." + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Open decrypts a value produced by Seal with the same aad.
func (k *Keyring) Open(value, aad string) (string, error) {
	if k == nil {
		return "", errors.New("crypto: nil keyring")
	}
	id, data, err := split(value)
	if err != nil {
		return "", err
	}
	aead, ok := k.keys[id]
	if !ok {
		return "", fmt.Errorf("%w: key %q not in ring", ErrOpen, id)
	}
	raw, err := base64.RawURLEncoding.DecodeString(data)
	if err != nil || len(raw) < aead.NonceSize() {
		return "", fmt.Errorf("%w: malformed", ErrOpen)
	}
	nonce, ct := raw[:aead.NonceSize()], raw[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, ct, k.aad(id, aad))
	if err != nil {
		return "", ErrOpen
	}
	return string(plain), nil
}

// NeedsRotation reports whether value was sealed with a key other than the
// primary. A malformed value reports true so a sweep surfaces it.
func (k *Keyring) NeedsRotation(value string) bool {
	id, _, err := split(value)
	return err != nil || id != k.primary
}

// The key id is part of the authenticated data, so a value cannot be
// relabelled to a different key.
func (k *Keyring) aad(keyID, aad string) []byte {
	return []byte(formatVersion + "|" + keyID + "|" + aad)
}

func split(value string) (keyID, data string, err error) {
	parts := strings.SplitN(value, ".", 3)
	if len(parts) != 3 || parts[0] != formatVersion || parts[1] == "" || parts[2] == "" {
		return "", "", fmt.Errorf("%w: malformed", ErrOpen)
	}
	return parts[1], parts[2], nil
}

// NewKey returns a fresh random key in standard base64, for tooling and tests.
func NewKey() string {
	b := make([]byte, KeySize)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}
