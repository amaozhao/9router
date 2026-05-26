// Package cryptox provides per-tenant envelope encryption and API key utilities.
// Byte-compatible with Node shared/src/crypto.js:
//
//	HKDF-SHA256:
//	  salt = "lazirouter-cloud-tenant"
//	  info = "tenant-<id>"
//	  ikm  = CLOUD_MASTER_KEY (32 raw bytes)
//	  out  = 32 bytes (AES-256 key)
//
//	Blob layout:
//	  [1B version=0x01] [12B IV] [16B GCM tag] [ciphertext...]
//
//	API key shape:
//	  "sk-lr-" + base64url(crypto.randomBytes(20))[:28]   (34 chars total)
package cryptox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"golang.org/x/crypto/hkdf"
)

const blobVersion byte = 0x01

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

// GenerateAPIKey mirrors the Node generator:
//
//	"sk-lr-" + base64url(rand 20 bytes).slice(0, 28)
//
// Returns the plaintext, its sha256 hex (for key_hash), and the first-14-char
// display prefix (sk-lr- + 8 chars).
func GenerateAPIKey() (plaintext, hashHex, keyPrefix string, err error) {
	raw := make([]byte, 20)
	if _, err = rand.Read(raw); err != nil {
		return "", "", "", err
	}
	// 20 bytes base64url (no padding) = 27 chars; matches Node generator.
	body := base64.RawURLEncoding.EncodeToString(raw)
	plaintext = "sk-lr-" + body
	hashHex = SHA256Hex(plaintext)
	keyPrefix = plaintext[:14]
	return
}

// deriveDEK runs HKDF-SHA256 with the Node-compatible salt and info.
func deriveDEK(masterKey []byte, tenantID int64) ([]byte, error) {
	salt := []byte("lazirouter-cloud-tenant")
	info := []byte("tenant-" + strconv.FormatInt(tenantID, 10))
	r := hkdf.New(sha256.New, masterKey, salt, info)
	out := make([]byte, 32)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, err
	}
	return out, nil
}

// EncryptForTenant produces a blob byte-compatible with the Node side.
//
//	[1B 0x01] [12B IV] [16B GCM tag] [ciphertext]
func EncryptForTenant(masterKey []byte, tenantID int64, plain any) ([]byte, error) {
	pt, err := json.Marshal(plain)
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
	iv := make([]byte, 12)
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}
	// AEAD.Seal returns ciphertext||tag; we have to split tag and reorder to
	// match Node's [version || iv || tag || ct] layout.
	sealed := aead.Seal(nil, iv, pt, nil)
	if len(sealed) < 16 {
		return nil, errors.New("sealed output too short")
	}
	ct := sealed[:len(sealed)-16]
	tag := sealed[len(sealed)-16:]
	out := make([]byte, 0, 1+12+16+len(ct))
	out = append(out, blobVersion)
	out = append(out, iv...)
	out = append(out, tag...)
	out = append(out, ct...)
	return out, nil
}

// DecryptForTenant inverts EncryptForTenant. Returns a generic map[string]any.
func DecryptForTenant(masterKey []byte, tenantID int64, blob []byte) (map[string]any, error) {
	if len(blob) < 1+12+16 {
		return nil, errors.New("encrypted blob too short")
	}
	if blob[0] != blobVersion {
		return nil, fmt.Errorf("unsupported crypto blob version: 0x%02x", blob[0])
	}
	iv := blob[1:13]
	tag := blob[13:29]
	ct := blob[29:]
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
	// Reconstruct the {ciphertext || tag} format AEAD.Open expects.
	sealed := make([]byte, 0, len(ct)+len(tag))
	sealed = append(sealed, ct...)
	sealed = append(sealed, tag...)
	pt, err := aead.Open(nil, iv, sealed, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(pt, &out); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w", err)
	}
	return out, nil
}
