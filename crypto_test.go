package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain redirects the secret-key directory to a temp dir for the
// whole test binary: without it the crypto helpers would generate a
// key in the real user data directory (and tests would depend on it).
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "crypto-test-key")
	if err == nil {
		old := secretKeyDir
		secretKeyDir = func() (string, error) { return dir, nil }
		code := m.Run()
		os.RemoveAll(dir)
		secretKeyDir = old
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// Round-trip: encrypt → decrypt yields the original plaintext.
func TestEncryptDecryptRoundTrip(t *testing.T) {
	// Use a temp dir for the key.
	tmp := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmp)
	defer os.Chdir(oldWd)

	plaintext := "hunter2"
	ct, err := encryptPassword(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if ct == plaintext {
		t.Error("ciphertext equals plaintext (no encryption happened)")
	}
	if !IsEncrypted(ct) {
		t.Errorf("IsEncrypted should be true for ciphertext %q", ct)
	}
	got, err := decryptPassword(ct)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != plaintext {
		t.Errorf("round-trip mismatch: got %q want %q", got, plaintext)
	}
}

// Empty passwords are no-ops (no encryption needed).
func TestEncryptEmptyPassword(t *testing.T) {
	tmp := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmp)
	defer os.Chdir(oldWd)

	got, err := encryptPassword("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
	got2, err := decryptPassword("")
	if err != nil {
		t.Fatal(err)
	}
	if got2 != "" {
		t.Errorf("decrypt empty: expected empty, got %q", got2)
	}
}

// Legacy plaintext (pre-encryption config) must still be readable so
// users on existing installs are not broken.
func TestDecryptAcceptsLegacyPlaintext(t *testing.T) {
	tmp := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmp)
	defer os.Chdir(oldWd)

	const legacy = "my-plain-password"
	got, err := decryptPassword(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if got != legacy {
		t.Errorf("legacy plaintext not preserved: got %q", got)
	}
	if IsEncrypted(legacy) {
		t.Error("legacy plaintext should not be flagged as encrypted")
	}
}

// After encryption, two encryptions of the same plaintext produce
// different ciphertexts (because of random nonce).
func TestEncryptionIsNondeterministic(t *testing.T) {
	tmp := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmp)
	defer os.Chdir(oldWd)

	const plaintext = "secret"
	a, _ := encryptPassword(plaintext)
	b, _ := encryptPassword(plaintext)
	if a == b {
		t.Errorf("two encryptions of same plaintext produced same ciphertext (nonce reuse?)")
	}
	// Both must decrypt to the same plaintext.
	pa, _ := decryptPassword(a)
	pb, _ := decryptPassword(b)
	if pa != plaintext || pb != plaintext {
		t.Errorf("decrypt mismatch: %q / %q", pa, pb)
	}
}

// Ciphertext must not contain the plaintext as a substring.
func TestCiphertextDoesNotLeakPlaintext(t *testing.T) {
	tmp := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmp)
	defer os.Chdir(oldWd)

	const plaintext = "supersecretpassword"
	ct, _ := encryptPassword(plaintext)
	if strings.Contains(ct, plaintext) {
		t.Errorf("ciphertext leaks plaintext: %q", ct)
	}
}

// The secret key file is created with 0600 permissions (Unix).
func TestSecretKeyFilePermissions(t *testing.T) {
	if os.Getenv("GOOS") == "windows" {
		t.Skip("unix file permissions not applicable")
	}
	tmp := t.TempDir()
	oldDir := secretKeyDir
	secretKeyDir = func() (string, error) { return tmp, nil }
	t.Cleanup(func() { secretKeyDir = oldDir })

	if _, err := loadOrCreateSecretKey(); err != nil {
		t.Fatal(err)
	}
	// Re-load to ensure existing key is reused.
	_, err := loadOrCreateSecretKey()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(tmp, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("secret key file perm = %o, want 0600", perm)
	}
}
