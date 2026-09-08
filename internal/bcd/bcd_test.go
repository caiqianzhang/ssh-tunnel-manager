package bcd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// signRequestVector is the expected Authorization header value for
// AK-test/SK-test, POST /v1/domain/resolve/list at 2026-09-03T00:00:00Z.
const signRequestVector = "bce-auth-v1/AK-test/2026-09-03T00:00:00Z/1800/host/" +
	"a00258c153b0f641b659621076d3133ca6950608606eff1d7729b7e2332a5d85"

func TestSignRequestMatchesReferenceVector(t *testing.T) {
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	got := SignRequest("AK-test", "SK-test", "POST", "/v1/domain/resolve/list", now)
	if got != signRequestVector {
		t.Errorf("signRequest mismatch:\n  got:  %s\n  want: %s", got, signRequestVector)
	}
}

// TestSignRequestDeterministic verifies the signature is stable for
// identical inputs (no randomness in the signing path).
func TestSignRequestDeterministic(t *testing.T) {
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	a := SignRequest("AK-test", "SK-test", "POST", "/v1/domain/resolve/list", now)
	b := SignRequest("AK-test", "SK-test", "POST", "/v1/domain/resolve/list", now)
	if a != b {
		t.Errorf("signRequest is not deterministic: %q vs %q", a, b)
	}
}

// TestSignRequestDifferentiatesInputs verifies that changing any input
// produces a different signature (catches copy-paste bugs).
func TestSignRequestDifferentiatesInputs(t *testing.T) {
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	base := SignRequest("AK-test", "SK-test", "POST", "/v1/domain/resolve/list", now)

	cases := []struct {
		name string
		got  string
	}{
		{"different AK", SignRequest("AK-other", "SK-test", "POST", "/v1/domain/resolve/list", now)},
		{"different SK", SignRequest("AK-test", "SK-other", "POST", "/v1/domain/resolve/list", now)},
		{"different method", SignRequest("AK-test", "SK-test", "GET", "/v1/domain/resolve/list", now)},
		{"different path", SignRequest("AK-test", "SK-test", "POST", "/v1/domain/resolve/list/foo", now)},
		{"different time", SignRequest("AK-test", "SK-test", "POST", "/v1/domain/resolve/list",
			now.Add(time.Second))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.got == base {
				t.Errorf("SignRequest(%s) == base, expected different signature", c.name)
			}
		})
	}
}

// TestSignRequestPrefix verifies the scheme prefix and /host/ segment
// are always present in the returned Authorization header.
func TestSignRequestPrefix(t *testing.T) {
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	got := SignRequest("AK-test", "SK-test", "POST", "/v1/domain/resolve/list", now)
	if !strings.HasPrefix(got, "bce-auth-v1/AK-test/") {
		t.Errorf("signRequest missing expected prefix, got %q", got)
	}
	if !strings.Contains(got, "/host/") {
		t.Errorf("signRequest missing /host/ segment, got %q", got)
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

	// Chinese full-width colon is accepted alongside ASCII ":".
	chinese := "Secret：SK-789\nkey：AK-012\n"
	pc := filepath.Join(dir, "chinese.key")
	if err := os.WriteFile(pc, []byte(chinese), 0o600); err != nil {
		t.Fatal(err)
	}
	ak, sk, err = LoadCredentials(pc)
	if err != nil {
		t.Fatalf("chinese colon key: %v", err)
	}
	if ak != "AK-012" || sk != "SK-789" {
		t.Errorf("got AK=%q SK=%q, want AK=AK-012 SK=SK-789", ak, sk)
	}

	// Lines without a colon are skipped, and unknown keys are ignored.
	sparse := "garbage line\nfoo: bar\nsecret: SK-1\nkey: AK-2\n"
	ps := filepath.Join(dir, "sparse.key")
	if err := os.WriteFile(ps, []byte(sparse), 0o600); err != nil {
		t.Fatal(err)
	}
	ak, sk, err = LoadCredentials(ps)
	if err != nil {
		t.Fatalf("sparse key: %v", err)
	}
	if ak != "AK-2" || sk != "SK-1" {
		t.Errorf("got AK=%q SK=%q, want AK=AK-2 SK=SK-1", ak, sk)
	}

	// Missing AK.
	onlySK := "secret: SK-123\n"
	p2 := filepath.Join(dir, "only-sk.key")
	if err := os.WriteFile(p2, []byte(onlySK), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadCredentials(p2); err == nil {
		t.Fatal("missing AK: expected error")
	}

	// Missing file.
	if _, _, err := LoadCredentials(filepath.Join(dir, "does-not-exist")); err == nil {
		t.Fatal("missing file: expected error")
	}
}
