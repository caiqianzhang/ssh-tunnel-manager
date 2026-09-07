// Package bcd implements the shared Baidu Cloud DNS (BCD) BCE Auth V1
// signing and credential parsing used by both the main app and the
// querydns CLI.
//
// Keeping the signing logic in one place avoids the drift that a
// copy-pasted canonicalURI/signRequest would silently introduce: a BCE
// signing bug fix has to land in exactly one place, not two.
package bcd

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// SignExpiration is the BCE Auth V1 signature validity window (seconds).
const SignExpiration = "1800"

// CanonicalURI percent-encodes path segments for BCE Auth V1 signing.
// It strips empty segments and encodes each byte that is not unreserved
// (ALPHA / DIGIT / "-" / "_" / "." / "~").
func CanonicalURI(path string) string {
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

// SignRequest generates a BCE Auth V1 Authorization header value.
//
//	Scheme: bce-auth-v1/{AK}/{timestamp}/{expiration}/host/{signature}
//
// Signing key derivation:
//  1. signing_key = HMAC-SHA256(SK, "bce-auth-v1/{AK}/{timestamp}/{expiration}")
//  2. signature   = HMAC-SHA256(signing_key, "{METHOD}\n{uri}\n\nhost:bcd.baidubce.com")
//
// The request body is NOT part of the canonical string.
func SignRequest(accessKey, secretKey, method, path string, now time.Time) string {
	timestamp := now.Format("2006-01-02T15:04:05Z")
	prefix := fmt.Sprintf("bce-auth-v1/%s/%s/%s", accessKey, timestamp, SignExpiration)
	canonical := fmt.Sprintf("%s\n%s\n\nhost:bcd.baidubce.com", method, CanonicalURI(path))

	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(prefix))
	signingKey := hex.EncodeToString(mac.Sum(nil))

	mac2 := hmac.New(sha256.New, []byte(signingKey))
	mac2.Write([]byte(canonical))
	signature := hex.EncodeToString(mac2.Sum(nil))

	return fmt.Sprintf("%s/host/%s", prefix, signature)
}

// LoadCredentials reads AK/SK from a baidu.key file.
//
// Expected format (one per line, Chinese or ASCII colon):
//
//	Secret：<your_secret_key>
//	key：<your_access_key>
//
// where "key" maps to AK and "Secret" maps to SK.
func LoadCredentials(path string) (ak, sk string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
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