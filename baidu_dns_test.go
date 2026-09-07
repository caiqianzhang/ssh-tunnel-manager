package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

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
