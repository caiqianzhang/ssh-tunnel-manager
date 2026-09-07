package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/ssh-tunnel-manager/ssh-tunnel-manager/internal/bcd"
)

// Baidu Cloud DNS (BCD) API integration.
//
// Ported from the Rust reference (docs/reference/baidu_dns.rs). Provides fast IP resolution
// for the DDNS scenario: when the server's IP changes, the Baidu DNS API
// returns the current IP immediately, bypassing DNS propagation delay
// (which is bounded by the record TTL).
//
// Credentials (AK/SK) are read from baidu.key — a local file that is
// git-ignored. If the file is missing or malformed, the integration
// silently disables itself and callers fall back to ordinary DNS.

// baiduDNSAPIBase is the Baidu Cloud BCD API endpoint. It is a var
// (not const) so tests can point QueryBaiduDNSIP at a mock server.
// Access is guarded by baiduBaseMu.
var (
	baiduDNSAPIBase = bcd.BaseURL
	baiduBaseMu     sync.RWMutex
)

// baiduAPIBase returns the current API base URL.
func baiduAPIBase() string {
	baiduBaseMu.RLock()
	defer baiduBaseMu.RUnlock()
	return baiduDNSAPIBase
}

// Cached Baidu credentials, loaded once at startup.
var (
	baiduAK string
	baiduSK string
	baiduMu sync.RWMutex
	baiduOK bool
)

// InitBaiduDNS loads Baidu DNS credentials from baidu.key.
// Searches the executable directory, config directory, and CWD.
// Silently disables itself if the file is absent or malformed.
func InitBaiduDNS() {
	path, err := findBaiduKey()
	if err != nil {
		Logf("Baidu DNS: baidu.key not found, API integration disabled")
		return
	}
	ak, sk, err := bcd.LoadCredentials(path)
	if err != nil {
		// LoadCredentials' error already quotes the path; don't repeat it.
		Logf("Baidu DNS: %v", err)
		return
	}
	baiduMu.Lock()
	baiduAK, baiduSK = ak, sk
	baiduOK = true
	baiduMu.Unlock()
	Logf("Baidu DNS: credentials loaded from %s (AK=%s)", path, maskAK(ak))
}

// baiduCredentials returns the cached Baidu AK/SR. Returns empty strings
// if credentials were not loaded.
func baiduCredentials() (ak, sk string, ok bool) {
	baiduMu.RLock()
	defer baiduMu.RUnlock()
	return baiduAK, baiduSK, baiduOK
}

// maskAK shows only the first 4 chars of the AK for log readability.
func maskAK(ak string) string {
	if len(ak) <= 4 {
		return "***"
	}
	return ak[:4] + "***"
}

// findBaiduKey searches for baidu.key in common locations.
func findBaiduKey() (string, error) {
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "baidu.key"))
	}
	if dir, err := appConfigDir(); err == nil {
		candidates = append(candidates, filepath.Join(dir, "baidu.key"))
	}
	candidates = append(candidates, "baidu.key")

	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("baidu.key not found")
}

// DnsTarget holds Baidu DNS API credentials and the target zone.
type DnsTarget struct {
	AccessKey string
	SecretKey string
	Zone      string
	APIBase   string
}

// NewDnsTarget creates a DnsTarget with the given credentials and zone.
func NewDnsTarget(accessKey, secretKey, zone, apiBase string) *DnsTarget {
	return &DnsTarget{
		AccessKey: accessKey,
		SecretKey: secretKey,
		Zone:      zone,
		APIBase:   strings.TrimSuffix(apiBase, "/"),
	}
}

// listRecords calls POST /v1/domain/resolve/list and returns the raw
// JSON records of the whole zone. Results are paginated (pageSize 100):
// a zone with more records than one page would otherwise silently hide
// its later entries — exactly the A record this feature needs.
func (d *DnsTarget) listRecords() ([]json.RawMessage, error) {
	const pageSize = 100
	const maxPages = 10 // 1000 records — plenty for a DDNS zone, and a
	//                    hard stop against a misbehaving API.

	// path and its canonical form are constant across the pagination
	// loop; compute them once instead of re-canonicalizing per page.
	path := "/v1/domain/resolve/list"
	canonicalURI := bcd.CanonicalURI(path)
	url := d.APIBase + canonicalURI

	var all []json.RawMessage
	for pageNo := 1; pageNo <= maxPages; pageNo++ {
		body := fmt.Sprintf(`{"domain":%q,"pageNo":%d,"pageSize":%d}`, d.Zone, pageNo, pageSize)

		req, err := http.NewRequest("POST", url, strings.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create DNS request: %w", err)
		}

		now := time.Now().UTC()
		req.Header.Set("Authorization", bcd.SignRequest(d.AccessKey, d.SecretKey, "POST", path, now))
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		req.Header.Set("Host", bcd.Host)

		// A fresh client per call: QueryBaiduDNSIP runs in the DDNS
		// heartbeat goroutine (every 15s) and on each SSH connect, but
		// http.DefaultTransport (the fallback when Transport is nil) is
		// already a process-global pooled transport, so an explicit
		// client here buys no extra connection reuse — it only adds a
		// mutable package-global for callers to trip over.
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			return nil, fmt.Errorf("请求百度云 DNS %s 失败: %w", path, err)
		}
		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("读取百度云 DNS %s 响应失败(HTTP %d): %w", path, resp.StatusCode, readErr)
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			var errPayload struct {
				Message string `json:"message"`
			}
			detail := strings.TrimSpace(string(respBody))
			if json.Unmarshal(respBody, &errPayload) == nil && errPayload.Message != "" {
				detail = errPayload.Message
			}
			return nil, fmt.Errorf("百度云 DNS %s HTTP %d: %s", path, resp.StatusCode, detail)
		}

		// Some endpoints (e.g. edit) return an empty body on success.
		if len(respBody) == 0 {
			break
		}

		var payload struct {
			Result []json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(respBody, &payload); err != nil {
			return nil, fmt.Errorf("百度云 DNS %s 响应解析失败(HTTP %d): %w", path, resp.StatusCode, err)
		}

		all = append(all, payload.Result...)
		if len(payload.Result) < pageSize {
			break // last page
		}
	}
	return all, nil
}

// QueryBaiduDNSIP queries the Baidu DNS API for the current A record IP
// of subdomain under zone. Returns the IP string or an error.
//
// Example: QueryBaiduDNSIP(ak, sk, "ruanjianggongcheng.site", "www")
// returns the current IP of www.ruanjianggongcheng.site.
func QueryBaiduDNSIP(accessKey, secretKey, zone, sub string) (string, error) {
	return QueryBaiduDNSIPWithBase(accessKey, secretKey, zone, sub, baiduAPIBase())
}

// QueryBaiduDNSIPWithBase is identical to QueryBaiduDNSIP but accepts an
// explicit API base URL. It exists so tests can point at a mock server
// without touching the production constant.
func QueryBaiduDNSIPWithBase(accessKey, secretKey, zone, sub, apiBase string) (string, error) {
	target := NewDnsTarget(accessKey, secretKey, zone, apiBase)
	records, err := target.listRecords()
	if err != nil {
		return "", err
	}

	for _, record := range records {
		var r struct {
			RecordID int    `json:"recordId"`
			Domain   string `json:"domain"`
			RDType   string `json:"rdtype"`
			RData    string `json:"rdata"`
			ZoneName string `json:"zoneName"`
			Status   string `json:"status"`
		}
		if err := json.Unmarshal(record, &r); err != nil {
			continue
		}
		// Match the subdomain (e.g. "www") and A/AAAA record type.
		// The API returns the host name in "domain" (not the full FQDN).
		if r.Domain == sub && r.Status == "RUNNING" && (r.RDType == "A" || r.RDType == "AAAA") {
			return r.RData, nil
		}
	}

	return "", fmt.Errorf("未找到 %s.%s 的 A 记录", sub, zone)
}

// ResolveRealIP returns the authoritative current A/AAAA record IP for
// host via the Baidu DNS API. ok is false when the integration is
// disabled (no baidu.key), host is already an IP literal, or the zone
// has no matching record — callers should fall back to ordinary DNS in
// all of those cases.
func ResolveRealIP(host string) (ip string, ok bool) {
	ak, sk, loaded := baiduCredentials()
	if !loaded || ak == "" || sk == "" {
		return "", false
	}
	// Already an IP literal: nothing to resolve, skip the API round trip.
	if net.ParseIP(host) != nil {
		return "", false
	}
	zone, sub := deriveZoneAndSub(host)
	ip, err := QueryBaiduDNSIP(ak, sk, zone, sub)
	if err != nil {
		Logf("ResolveRealIP: Baidu DNS query failed for %s: %v", host, err)
		return "", false
	}
	return ip, true
}

// deriveZoneAndSub splits a hostname into zone (registrable domain) and
// subdomain, using the public suffix list rather than a hand-rolled
// table. A hand-rolled table silently misclassifies any suffix it omits
// (e.g. "ac.nz", "co.id"), producing a zone that is not a registrable
// domain and causing the Baidu API query to target a nonexistent zone
// — the DDNS fast-path then silently falls back to ordinary DNS.
//
//	"www.example.com"  -> zone="example.com",       sub="www"
//	"example.com"      -> zone="example.com",       sub="@"
//	"a.b.example.com"  -> zone="example.com",       sub="a.b"
//	"www.bbc.co.uk"    -> zone="bbc.co.uk",         sub="www"
//	"site.co.uk"       -> zone="site.co.uk",        sub="@"
func deriveZoneAndSub(host string) (zone, sub string) {
	// Normalize: case-insensitive, and a trailing dot would corrupt the
	// label split (EffectiveTLDPlusOne rejects empty labels).
	host = strings.ToLower(strings.TrimSuffix(host, "."))

	zone, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		// Single-label host (e.g. "localhost") or an IP literal: there
		// is no registrable domain, so treat the whole host as the zone.
		return host, "@"
	}
	if host == zone {
		return zone, "@"
	}
	return zone, strings.TrimSuffix(host, "."+zone)
}
