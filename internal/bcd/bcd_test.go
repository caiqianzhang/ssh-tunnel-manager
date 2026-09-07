package bcd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// signRequestVector is the expected Authorization header value for
// AK-test/SK-test, POST /v1/domain/resolve/list at 2026-09-03T00:00:00Z.
// Cross-checked against the Rust reference (docs/reference/baidu_dns.rs).
const signRequestVector = "bce-auth-v1/AK-test/2026-09-03T00:00:00Z/1800/host/" +
	"a00258c153b0f641b659621076d3133ca6950608606eff1d7729b7e2332a5d85"

func TestSignRequestMatchesReferenceVector(t *testing.T) {
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	got := SignRequest("AK-test", "SK-test", "POST", "/v1/domain/resolve/list", now)
	if got != signRequestVector {
		t.Errorf("signRequest mismatch:\n  got:  %s\n  want: %s", got, signRequestVector)
	}
}

func TestCanonicalURI(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain path", "/v1/domain/resolve/list", "/v1/domain/resolve/list"},
		{"encodes space", "/a b/c", "/a%20b/c"},
		{"strips trailing slash", "/x/", "/x"},
		{"strips empty segments", "/a//b///c/", "/a/b/c"},
		{"encodes non-unreserved", "/foo bar/baz!", "/foo%20bar/baz%21"},
		{"preserves unreserved chars", "/a-b_c.d~e", "/a-b_c.d~e"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanonicalURI(tt.in); got != tt.want {
				t.Errorf("CanonicalURI(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestLoadCredentials(t *testing.T) {
	dir := t.TempDir()

	// Valid key, both Chinese and ASCII colons accepted.
	valid := "Secret: SK-123\nkey: AK-456\n"
	p := filepath.Join(dir, "baidu.key")
	if err := os.WriteFile(p, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	ak, sk, err := LoadCredentials(p)
	if err != nil {
		t.Fatalf("valid key: %v", err)
	}
	if ak != "AK-456" || sk != "SK-123" {
		t.Errorf("got AK=%q SK=%q, want AK=AK-456 SK=SK-123", ak, sk)
	}

	// Missing AK.
	onlySK := "secret: SK-123\n"
	p2 := filepath.Join(dir, "only-sk.key")
	if err := os.WriteFile(p2, []byte(onlySK), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadCredentials(p2); err == nil {
		t.Fatal("missing AK: expected error")
	} else if !os.IsNotExist(err) && err.Error() == "" {
		t.Fatal("missing AK: expected a non-empty error")
	}

	// Missing file.
	if _, _, err := LoadCredentials(filepath.Join(dir, "does-not-exist")); err == nil {
		t.Fatal("missing file: expected error")
	}
}