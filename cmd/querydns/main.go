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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ssh-tunnel-manager/ssh-tunnel-manager/internal/bcd"
)

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
			return bcd.LoadCredentials(p)
		}
	}
	return "", "", fmt.Errorf("baidu.key not found (looked next to the executable and in the working directory)")
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

	apiBase := bcd.BaseURL

	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy: nil, // bypass system proxy
		},
	}

	// Query ALL records for the domain
	path := "/v1/domain/resolve/list"
	url := apiBase + bcd.CanonicalURI(path)

	// Marshal the domain value with json.Marshal rather than fmt %q: %q
	// applies Go string escaping (\xNN), which is not valid JSON, so a
	// zone containing a raw control byte or invalid UTF-8 byte would
	// produce a body the API rejects as HTTP 400.
	bodyBytes, err := json.Marshal(map[string]interface{}{
		"domain":  zone,
		"pageNo":  1,
		"pageSize": 100,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "querydns: marshal request body: %v\n", err)
		os.Exit(1)
	}
	body := string(bodyBytes)

	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "querydns: create request: %v\n", err)
		os.Exit(1)
	}

	now := time.Now().UTC()
	req.Header.Set("Authorization", bcd.SignRequest(ak, sk, "POST", path, now))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Host", bcd.Host)

	// Request context goes to stderr: it is debugging scaffolding, not
	// the data a piping consumer reads.
	fmt.Fprintf(os.Stderr, "=== Request ===\nPOST %s\nBody: %s\n\n", url, body)

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "querydns: request failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "querydns: read response: %v\n", err)
		os.Exit(1)
	}

	// Non-2xx is an API error, not a result set: report it on stderr so a
	// wrapper that captures stdout (output=$(querydns ...)) does not get
	// Baidu's error envelope mixed into the record list.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "=== Response (HTTP %d) ===\n%s\n", resp.StatusCode, string(respBody))
		os.Exit(1)
	}

	// Parse and display records.
	var payload struct {
		Result []json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(respBody, &payload); err != nil {
		fmt.Fprintf(os.Stderr, "querydns: parse error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("=== Records (%d) ===\n", len(payload.Result))
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
		if err := json.Unmarshal(r, &rec); err != nil {
			fmt.Fprintf(os.Stderr, "querydns: record[%d] parse error: %v\n", i, err)
			continue
		}
		fmt.Printf("[%d] domain=%s rdtype=%s rdata=%s status=%s ttl=%d\n",
			i, rec.Domain, rec.RDType, rec.RData, rec.Status, rec.TTL)
	}
}
