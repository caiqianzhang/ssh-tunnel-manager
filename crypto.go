package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// secretKeyPath is the location of the AES-256 key used to encrypt
// stored SSH passwords. The key is auto-generated on first run and
// stored with 0600 permissions.
//
// We deliberately do NOT use a passphrase-derived key because the goal
// is only to avoid plaintext-on-disk, not high-grade secrets. The threat
// model: a casual user syncing config.json to a cloud drive or sharing
// it in chat. Anyone with read access to BOTH the config file AND the
// key file can decrypt — but at that point the attacker already has
// local shell access, which beats any per-app encryption anyway.
// secretKeyDir resolves the directory holding the key file; a
// variable so tests can redirect it to a temp directory.
var secretKeyDir = appDataDir

// loadOrCreateSecretKey returns a 32-byte AES-256 key, creating a
// new one if none exists. The key lives in the per-user data
// directory (see paths.go) — deliberately NOT next to config.json.
func loadOrCreateSecretKey() ([]byte, error) {
	dir, err := secretKeyDir()
	if err != nil {
		return nil, fmt.Errorf("resolve secret key dir: %w", err)
	}
	keyPath := filepath.Join(dir, "secret.key")
	data, err := os.ReadFile(keyPath)
	if err == nil {
		if len(data) != 32 {
			return nil, fmt.Errorf("secret key has wrong size: %d bytes", len(data))
		}
		return data, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read secret key: %w", err)
	}
	// Generate fresh key.
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate secret key: %w", err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create secret dir: %w", err)
	}
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		return nil, fmt.Errorf("write secret key: %w", err)
	}
	return key, nil
}

// encryptPassword encrypts a plaintext password using AES-256-GCM and
// returns a base64 string of (nonce || ciphertext). If plaintext is
// empty, returns "" unchanged.
func encryptPassword(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	key, err := loadOrCreateSecretKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("new gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("gen nonce: %w", err)
	}
	ct := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	out := append(nonce, ct...)
	return base64.StdEncoding.EncodeToString(out), nil
}

// decryptPassword reverses encryptPassword. If cipherText is empty,
// returns "" unchanged. Values that don't look like ciphertext (legacy
// plaintext from pre-encryption configs) pass through unchanged; values
// that DO look like ciphertext but fail validation return an error.
func decryptPassword(cipherText string) (string, error) {
	if cipherText == "" {
		return "", nil
	}
	// Gate on the ciphertext shape (base64, at least nonce+tag bytes):
	// without it, a legacy plaintext password that happens to be valid
	// base64 (e.g. a 16-char alphanumeric string) would decode, fail GCM
	// validation and take the whole config down with a read-only error.
	if !IsEncrypted(cipherText) {
		return cipherText, nil
	}
	key, err := loadOrCreateSecretKey()
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(cipherText)
	if err != nil {
		return "", fmt.Errorf("decrypt password: invalid base64: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("new gcm: %w", err)
	}
	nonce := raw[:gcm.NonceSize()]
	ct := raw[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		// Valid-shape ciphertext that won't open: the secret key changed
		// or the data is corrupt — an error beats silently passing
		// garbage to SSH (confusing "Permission denied").
		return "", fmt.Errorf("decrypt password: GCM open failed (wrong key or corrupt data): %w", err)
	}
	return string(pt), nil
}

// IsEncrypted reports whether cipherText looks like an encrypted
// password (base64 with at least nonce + 16-byte AES-GCM tag length).
func IsEncrypted(cipherText string) bool {
	if cipherText == "" {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(cipherText)
	if err != nil {
		return false
	}
	// AES-GCM with 12-byte nonce = minimum 28 bytes ciphertext.
	return len(raw) >= 28
}

