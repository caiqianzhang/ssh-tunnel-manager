package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	"unicode/utf8"
)

// Baidu Cloud DNS (BCD) API integration.
//
// Ported from baidu_dns.rs (Rust reference). Provides fast IP resolution
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
	baiduDNSAPIBase = "https://bcd.baidubce.com"
	baiduBaseMu     sync.RWMutex
)

// baiduAPIBase returns the current API base URL.
func baiduAPIBase() string {
	baiduBaseMu.RLock()
	defer baiduBaseMu.RUnlock()
	return baiduDNSAPIBase
}

// setBaiduAPIBase overrides the API base URL. Used by tests.
func setBaiduAPIBase(url string) {
	baiduBaseMu.Lock()
	baiduDNSAPIBase = url
	baiduBaseMu.Unlock()
}

// signExpiration is the BCE Auth V1 signature validity window (seconds).
const signExpiration = "1800"

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
	ak, sk, err := LoadBaiduCredentials(path)
	if err != nil {
		Logf("Baidu DNS: failed to load credentials from %s: %v", path, err)
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

// LoadBaiduCredentials reads AK/SK from a baidu.key file.
//
// Expected format (one per line, Chinese or ASCII colon):
//
//	Secret：1bf13f09a545436faef3125e5aadf2d7
//	key：ALTAKNkj6MSaxHg4WHRVGnYHzx
//
// where "key" maps to AK and "Secret" maps to SK.
func LoadBaiduCredentials(path string) (ak, sk string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("read baidu.key: %w", err)
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Handle both Chinese "：" (U+FF1A, 3 bytes in UTF-8) and ASCII ":".
		var key, value string
		if idx := strings.Index(line, "："); idx >= 0 {
			key = strings.TrimSpace(line[:idx])
			_, size := utf8.DecodeRuneInString(line[idx:])
			value = strings.TrimSpace(line[idx+size:])
		} else if idx := strings.Index(line, ":"); idx >= 0 {
			key = strings.TrimSpace(line[:idx])
			value = strings.TrimSpace(line[idx+1:])
		} else {
			continue
		}

		switch strings.ToLower(key) {
		case "key", "ak":
			ak = value
		case "secret", "sk":
			sk = value
		}
	}

	if ak == "" || sk == "" {
		return "", "", fmt.Errorf("baidu.key missing AK or SK")
	}
	return ak, sk, nil
}

// canonicalURI percent-encodes path segments for BCE Auth V1 signing.
// Mirrors the Rust canonical_uri: strips empty segments, encodes each
// byte that is not unreserved (ALPHA / DIGIT / "-" / "_" / "." / "~").
func canonicalURI(path string) string {
	var parts []string
	for _, seg := range strings.Split(path, "/") {
		if seg == "" {
			continue
		}
		var out strings.Builder
		for _, b := range []byte(seg) {
			if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
				(b >= '0' && b <= '9') ||
				b == '-' || b == '_' || b == '.' || b == '~' {
				out.WriteByte(b)
			} else {
				out.WriteString(fmt.Sprintf("%%%02X", b))
			}
		}
		parts = append(parts, out.String())
	}
	return "/" + strings.Join(parts, "/")
}

// signRequest generates a BCE Auth V1 Authorization header value.
//
// Scheme: bce-auth-v1/{AK}/{timestamp}/{expiration}/host/{signature}
//
// Signing key derivation:
//  1. signing_key = HMAC-SHA256(SK, "bce-auth-v1/{AK}/{timestamp}/{expiration}")
//  2. signature   = HMAC-SHA256(signing_key, "{METHOD}\n{uri}\n\nhost:bcd.baidubce.com")
//
// The request body is NOT part of the canonical string (matches the
// Rust reference and the Baidu API specification).
func signRequest(accessKey, secretKey, method, path string, now time.Time) string {
	timestamp := now.Format("2006-01-02T15:04:05Z")
	prefix := fmt.Sprintf("bce-auth-v1/%s/%s/%s", accessKey, timestamp, signExpiration)
	canonical := fmt.Sprintf("%s\n%s\n\nhost:bcd.baidubce.com", method, canonicalURI(path))

	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(prefix))
	signingKey := hex.EncodeToString(mac.Sum(nil))

	mac2 := hmac.New(sha256.New, []byte(signingKey))
	mac2.Write([]byte(canonical))
	signature := hex.EncodeToString(mac2.Sum(nil))

	return fmt.Sprintf("%s/host/%s", prefix, signature)
}

// DnsTarget holds Baidu DNS API credentials and the target zone.
type DnsTarget struct {
	AccessKey string
	SecretKey string
	Zone      string
	Sub       string
	APIBase   string
}

// NewDnsTarget creates a DnsTarget with the given credentials and zone.
func NewDnsTarget(accessKey, secretKey, zone, sub, apiBase string) *DnsTarget {
	return &DnsTarget{
		AccessKey: accessKey,
		SecretKey: secretKey,
		Zone:      zone,
		Sub:       sub,
		APIBase:   strings.TrimSuffix(apiBase, "/"),
	}
}

// listRecords calls POST /v1/domain/resolve/list and returns the raw
// JSON record array from the "result" field.
func (d *DnsTarget) listRecords(client *http.Client) ([]json.RawMessage, error) {
	path := "/v1/domain/resolve/list"
	body := fmt.Sprintf(`{"domain":"%s","pageNo":1,"pageSize":100}`, d.Zone)

	req, err := http.NewRequest("POST", d.APIBase+canonicalURI(path), strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create DNS request: %w", err)
	}

	now := time.Now().UTC()
	req.Header.Set("Authorization", signRequest(d.AccessKey, d.SecretKey, "POST", path, now))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Host", "bcd.baidubce.com")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求百度云 DNS %s 失败: %w", path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取百度云 DNS %s 响应失败(HTTP %d): %w", path, resp.StatusCode, err)
	}

	// Some endpoints (e.g. edit) return an empty body on success.
	if strings.TrimSpace(string(respBody)) == "" {
		return nil, nil
	}

	var payload struct {
		Result []json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(respBody, &payload); err != nil {
		return nil, fmt.Errorf("百度云 DNS %s 响应解析失败(HTTP %d): %w", path, resp.StatusCode, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errPayload struct {
			Message string `json:"message"`
		}
		json.Unmarshal(respBody, &errPayload)
		return nil, fmt.Errorf("百度云 DNS %s HTTP %d: %s", path, resp.StatusCode, errPayload.Message)
	}

	return payload.Result, nil
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
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	target := NewDnsTarget(accessKey, secretKey, zone, sub, apiBase)
	records, err := target.listRecords(client)
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

// deriveZoneAndSub splits a hostname into zone (registered domain) and
// subdomain. Heuristic: the zone is the last two labels, the subdomain
// is everything before that.
//
//	"www.example.com"  -> zone="example.com",  sub="www"
//	"example.com"      -> zone="example.com",  sub="@"
//	"a.b.example.com"  -> zone="example.com",  sub="a.b"
func deriveZoneAndSub(host string) (zone, sub string) {
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return host, "@"
	}
	zone = strings.Join(labels[len(labels)-2:], ".")
	if len(labels) == 2 {
		sub = "@"
	} else {
		sub = strings.Join(labels[:len(labels)-2], ".")
	}
	return zone, sub
}
