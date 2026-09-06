// Command querydns queries Baidu Cloud DNS (BCD) for all resolution
// records of a zone and prints them — a debugging aid for the DDNS
// fast-path in the main app.
//
// Credentials are read from baidu.key ("key: ..." / "Secret: ...",
// Chinese or ASCII colon), searched next to the executable and in the
// working directory, so no secret is embedded in source.
//
// Usage: querydns <zone>   (e.g.: querydns example.com)
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const signExpiration = "1800"

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

// loadCredentials finds baidu.key (executable dir first, then CWD) and
// parses the AK/SK lines out of it.
func loadCredentials() (ak, sk string, err error) {
	var candidates []string
	if exe, e := os.Executable(); e == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "baidu.key"))
	}
	candidates = append(candidates, "baidu.key")

	for _, p := range candidates {
		if info, e := os.Stat(p); e == nil && !info.IsDir() {
			return parseBaiduKey(p)
		}
	}
	return "", "", fmt.Errorf("baidu.key not found (looked next to the executable and in the working directory)")
}

// parseBaiduKey reads AK/SK from a baidu.key file. "key" maps to AK and
// "Secret" maps to SK; both Chinese and ASCII colons are accepted.
func parseBaiduKey(path string) (string, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}

	var ak, sk string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
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
		return "", "", fmt.Errorf("%s missing AK or SK", path)
	}
	return ak, sk, nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法: querydns <zone>   (例如: querydns example.com)")
		os.Exit(2)
	}
	zone := os.Args[1]

	ak, sk, err := loadCredentials()
	if err != nil {
		fmt.Fprintf(os.Stderr, "querydns: %v\n", err)
		os.Exit(1)
	}

	apiBase := "https://bcd.baidubce.com"

	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy: nil, // bypass system proxy
		},
	}

	// Query ALL records for the domain
	path := "/v1/domain/resolve/list"
	body := fmt.Sprintf(`{"domain":"%s","pageNo":1,"pageSize":100}`, zone)

	req, err := http.NewRequest("POST", apiBase+canonicalURI(path), strings.NewReader(body))
	if err != nil {
		fmt.Printf("create request: %v\n", err)
		return
	}

	now := time.Now().UTC()
	req.Header.Set("Authorization", signRequest(ak, sk, "POST", path, now))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Host", "bcd.baidubce.com")

	fmt.Printf("=== Request ===\nPOST %s\nBody: %s\n\n",
		apiBase+canonicalURI(path), body)

	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("request failed: %v\n", err)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	fmt.Printf("=== Response (HTTP %d) ===\n%s\n", resp.StatusCode, string(respBody))

	// Parse and display records
	var payload struct {
		Result []json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(respBody, &payload); err != nil {
		fmt.Printf("parse error: %v\n", err)
		return
	}

	fmt.Printf("\n=== Records (%d) ===\n", len(payload.Result))
	for i, r := range payload.Result {
		var rec struct {
			RecordID int    `json:"recordId"`
			Domain   string `json:"domain"`
			RDType   string `json:"rdtype"`
			RData    string `json:"rdata"`
			ZoneName string `json:"zoneName"`
			Status   string `json:"status"`
			TTL      int    `json:"ttl"`
		}
		json.Unmarshal(r, &rec)
		fmt.Printf("[%d] domain=%s rdtype=%s rdata=%s status=%s ttl=%d\n",
			i, rec.Domain, rec.RDType, rec.RData, rec.Status, rec.TTL)
	}
}
