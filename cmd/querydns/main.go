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

	req, err := http.NewRequest("POST", apiBase+bcd.CanonicalURI(path), strings.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "querydns: create request: %v\n", err)
		os.Exit(1)
	}

	now := time.Now().UTC()
	req.Header.Set("Authorization", bcd.SignRequest(ak, sk, "POST", path, now))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Host", "bcd.baidubce.com")

	fmt.Printf("=== Request ===\nPOST %s\nBody: %s\n\n",
		apiBase+bcd.CanonicalURI(path), body)

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "querydns: request failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	fmt.Printf("=== Response (HTTP %d) ===\n%s\n", resp.StatusCode, string(respBody))

	// Parse and display records
	var payload struct {
		Result []json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(respBody, &payload); err != nil {
		fmt.Fprintf(os.Stderr, "querydns: parse error: %v\n", err)
		os.Exit(1)
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
