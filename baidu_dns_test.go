package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ssh-tunnel-manager/ssh-tunnel-manager/internal/bcd"
)

// ---------- canonicalURI ----------

func TestCanonicalURI(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain path",
			in:   "/v1/domain/resolve/list",
			want: "/v1/domain/resolve/list",
		},
		{
			name: "encodes space",
			in:   "/a b/c",
			want: "/a%20b/c",
		},
		{
			name: "strips trailing slash",
			in:   "/x/",
			want: "/x",
		},
		{
			name: "strips empty segments",
			in:   "/a//b///c/",
			want: "/a/b/c",
		},
		{
			name: "encodes non-unreserved",
			in:   "/foo bar/baz!",
			want: "/foo%20bar/baz%21",
		},
		{
			name: "preserves unreserved chars",
			in:   "/a-b_c.d~e",
			want: "/a-b_c.d~e",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bcd.CanonicalURI(tt.in)
			if got != tt.want {
				t.Errorf("bcd.CanonicalURI(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// ---------- signRequest (BCE Auth V1) ----------

// signRequestVector is the expected Authorization header value for
// AK-test/SK-test, POST /v1/domain/resolve/list at 2026-09-03T00:00:00Z.
// Cross-checked against the Rust reference (docs/reference/baidu_dns.rs)
// and the Python reference implementation (baidu_dns.py).
const signRequestVector = "bce-auth-v1/AK-test/2026-09-03T00:00:00Z/1800/host/" +
	"a00258c153b0f641b659621076d3133ca6950608606eff1d7729b7e2332a5d85"

func TestSignRequestMatchesReferenceVector(t *testing.T) {
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	got := bcd.SignRequest("AK-test", "SK-test", "POST", "/v1/domain/resolve/list", now)
	if got != signRequestVector {
		t.Errorf("signRequest mismatch:\n  got:  %s\n  want: %s", got, signRequestVector)
	}
}

// TestSignRequestDeterministic verifies the signature is stable for
// identical inputs (no randomness in the signing path).
func TestSignRequestDeterministic(t *testing.T) {
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	a := bcd.SignRequest("AK-test", "SK-test", "POST", "/v1/domain/resolve/list", now)
	b := bcd.SignRequest("AK-test", "SK-test", "POST", "/v1/domain/resolve/list", now)
	if a != b {
		t.Errorf("signRequest is not deterministic: %q vs %q", a, b)
	}
}

// TestSignRequestDifferentiatesInputs verifies that changing any input
// produces a different signature (catches copy-paste bugs).
func TestSignRequestDifferentiatesInputs(t *testing.T) {
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	base := bcd.SignRequest("AK-test", "SK-test", "POST", "/v1/domain/resolve/list", now)

	cases := []struct {
		name string
		got  string
	}{
		{"different AK", bcd.SignRequest("AK-other", "SK-test", "POST", "/v1/domain/resolve/list", now)},
		{"different SK", bcd.SignRequest("AK-test", "SK-other", "POST", "/v1/domain/resolve/list", now)},
		{"different method", bcd.SignRequest("AK-test", "SK-test", "GET", "/v1/domain/resolve/list", now)},
		{"different path", bcd.SignRequest("AK-test", "SK-test", "POST", "/v1/domain/resolve/list/foo", now)},
		{"different time", bcd.SignRequest("AK-test", "SK-test", "POST", "/v1/domain/resolve/list",
			now.Add(time.Second))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.got == base {
				t.Errorf("bcd.SignRequest(%s) == base, expected different signature", c.name)
			}
		})
	}
}

// TestSignRequestPrefix verifies the scheme prefix and /host/ segment
// are always present in the returned Authorization header.
func TestSignRequestPrefix(t *testing.T) {
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	got := bcd.SignRequest("AK-test", "SK-test", "POST", "/v1/domain/resolve/list", now)
	if !strings.HasPrefix(got, "bce-auth-v1/AK-test/") {
		t.Errorf("signRequest missing expected prefix, got %q", got)
	}
	if !strings.Contains(got, "/host/") {
		t.Errorf("signRequest missing /host/ segment, got %q", got)
	}
}

// ---------- deriveZoneAndSub ----------

func TestDeriveZoneAndSub(t *testing.T) {
	tests := []struct {
		host     string
		wantZone string
		wantSub  string
	}{
		{"www.example.com", "example.com", "www"},
		{"example.com", "example.com", "@"},
		{"a.b.example.com", "example.com", "a.b"},
		{"localhost", "localhost", "@"},
		{"single", "single", "@"},
		// Two-label public suffixes: the zone is three labels.
		{"www.bbc.co.uk", "bbc.co.uk", "www"},
		{"site.co.uk", "site.co.uk", "@"},
		{"mail.example.com.cn", "example.com.cn", "mail"},
		// Unknown two-label combos fall back to the two-label rule.
		{"sub.domain.co.xy", "co.xy", "sub.domain"},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			zone, sub := deriveZoneAndSub(tt.host)
			if zone != tt.wantZone {
				t.Errorf("deriveZoneAndSub(%q) zone = %q, want %q", tt.host, zone, tt.wantZone)
			}
			if sub != tt.wantSub {
				t.Errorf("deriveZoneAndSub(%q) sub = %q, want %q", tt.host, sub, tt.wantSub)
			}
		})
	}
}

// ---------- 分页 ----------

// TestQueryBaiduDNSIPPaginates pins the pagination fix: a zone whose
// first page (100 records) does not contain the target must be followed
// to the next page instead of silently missing the A record.
func TestQueryBaiduDNSIPPaginates(t *testing.T) {
	const pageSize = 100
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var body struct {
			PageNo int `json:"pageNo"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		var result []map[string]interface{}
		if body.PageNo == 1 {
			// Full page of non-matching filler records.
			for i := 0; i < pageSize; i++ {
				result = append(result, map[string]interface{}{
					"recordId": i, "domain": "filler", "rdtype": "A",
					"rdata": "0.0.0.0", "zoneName": "z.example", "status": "RUNNING",
				})
			}
		} else {
			// Short final page carrying the record we need.
			result = append(result, map[string]interface{}{
				"recordId": 999, "domain": "www", "rdtype": "A",
				"rdata": "9.9.9.9", "zoneName": "z.example", "status": "RUNNING",
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"result": result})
	}))
	defer server.Close()

	ip, err := QueryBaiduDNSIPWithBase("AK", "SK", "z.example", "www", server.URL)
	if err != nil {
		t.Fatalf("QueryBaiduDNSIP failed: %v", err)
	}
	if ip != "9.9.9.9" {
		t.Errorf("expected 9.9.9.9 from the second page, got %s", ip)
	}
	if n := requests.Load(); n != 2 {
		t.Errorf("expected exactly 2 page requests, got %d", n)
	}
}
