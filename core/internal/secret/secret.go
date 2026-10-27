// Package secret provides AES-256-GCM encryption at rest (key from BOXAZ_MASTER_KEY),
// random ids/tokens and key hashing.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Box encrypts/decrypts small values.
type Box struct{ aead cipher.AEAD }

// New derives a 32-byte key via SHA-256 from any master string (hex/base64/passphrase).
func New(master string) (*Box, error) {
	if master == "" {
		return nil, errors.New("empty master key")
	}
	k := sha256.Sum256([]byte(master))
	blk, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, err
	}
	a, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return &Box{aead: a}, nil
}

// LoadOrCreateMaster returns BOXAZ_MASTER_KEY or a key persisted at path (created 0600).
func LoadOrCreateMaster(env, path string) (string, bool, error) {
	if env != "" {
		return env, false, nil
	}
	if b, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(b)), false, nil
	}
	k := RandHex(32)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(path, []byte(k+"\n"), 0o600); err != nil {
		return "", false, err
	}
	return k, true, nil
}

// Seal returns "v1:" + base64(nonce|ciphertext).
func (b *Box) Seal(plain string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := b.aead.Seal(nonce, nonce, []byte(plain), nil)
	return "v1:" + base64.StdEncoding.EncodeToString(ct), nil
}

func (b *Box) Open(enc string) (string, error) {
	if !strings.HasPrefix(enc, "v1:") {
		return "", fmt.Errorf("unknown ciphertext version")
	}
	raw, err := base64.StdEncoding.DecodeString(enc[3:])
	if err != nil {
		return "", err
	}
	ns := b.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("ciphertext too short")
	}
	pt, err := b.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", errors.New("decrypt failed (wrong BOXAZ_MASTER_KEY?)")
	}
	return string(pt), nil
}

// RandHex returns n random bytes hex-encoded.
func RandHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"

// ID returns prefix_ + 12 random base32-ish chars.
func ID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return prefix + "_" + string(b)
}

// Hash returns a hex SHA-256 of a token (keys are high-entropy, so no KDF needed).
func Hash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// NewAPIKey returns the full key and its display prefix.
func NewAPIKey(kind string) (key, prefix string) {
	key = "bx_" + kind + "_" + RandHex(24)
	return key, key[:len("bx_"+kind+"_")+6]
}
