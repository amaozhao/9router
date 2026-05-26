// Package cryptox provides per-tenant envelope encryption and API key utilities.
// Compatible with the Node shared/src/crypto.js scheme: HKDF-SHA256 derives a
// per-tenant DEK from CLOUD_MASTER_KEY + tenant_id, then AES-256-GCM encrypts
// the plaintext. Wire format: nonce(12) || ciphertext || tag(16).
package cryptox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"golang.org/x/crypto/hkdf"
)

// SHA256Hex returns the lowercase hex sha256 of s.
func SHA256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// HMACSHA256Hex returns hex hmac-sha256(key, msg).
func HMACSHA256Hex(key, msg []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return hex.EncodeToString(m.Sum(nil))
}

// GenerateAPIKey returns a fresh `sk-lr-<22 chars>` token plus its sha256 hex
// hash (for storage in api_keys.key_hash) and the human-readable key_prefix
// (first 14 chars, used for display only).
func GenerateAPIKey() (plaintext, hashHex, keyPrefix string, err error) {
	raw := make([]byte, 16)
	if _, err = rand.Read(raw); err != nil {
		return "", "", "", err
	}
	// base32 without padding gives URL-safe 26-char string; we use the first 22
	// for visual symmetry with the Node version.
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	if len(enc) < 22 {
		return "", "", "", errors.New("base32 encode shorter than expected")
	}
	plaintext = "sk-lr-" + enc[:22]
	hashHex = SHA256Hex(plaintext)
	keyPrefix = plaintext[:14] // sk-lr- + 8 chars
	return
}

// deriveDEK runs HKDF-SHA256(masterKey, salt=tenantID, info="lazirouter-dek").
// Returns a 32-byte AES-256 key.
func deriveDEK(masterKey []byte, tenantID int64) ([]byte, error) {
	salt := []byte("tenant:" + strconv.FormatInt(tenantID, 10))
	r := hkdf.New(sha256.New, masterKey, salt, []byte("lazirouter-dek"))
	dek := make([]byte, 32)
	if _, err := io.ReadFull(r, dek); err != nil {
		return nil, err
	}
	return dek, nil
}

// EncryptForTenant turns a Go value (typically a map[string]any of credentials)
// into a JSON-then-AES-256-GCM-encrypted byte slice. Output:
//
//	nonce(12) || ciphertext || tag(16)
//
// This matches the byte layout the Node side reads back.
func EncryptForTenant(masterKey []byte, tenantID int64, plain any) ([]byte, error) {
	jsonBytes, err := json.Marshal(plain)
	if err != nil {
		return nil, fmt.Errorf("json marshal: %w", err)
	}
	dek, err := deriveDEK(masterKey, tenantID)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	// Seal appends ciphertext+tag to dst (which starts with nonce already).
	out := aead.Seal(nonce, nonce, jsonBytes, nil)
	return out, nil
}

// DecryptForTenant reverses EncryptForTenant. Returns the decoded JSON as a
// generic map. Caller can re-marshal into a typed struct.
func DecryptForTenant(masterKey []byte, tenantID int64, blob []byte) (map[string]any, error) {
	if len(blob) < 12+16 {
		return nil, errors.New("encrypted blob too short")
	}
	dek, err := deriveDEK(masterKey, tenantID)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce, ct := blob[:aead.NonceSize()], blob[aead.NonceSize():]
	pt, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(pt, &out); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w", err)
	}
	return out, nil
}
