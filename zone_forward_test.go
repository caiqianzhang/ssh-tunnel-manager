package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGenerateZoneForwardConfig pins the drop-in rendering: NS IPs as
// DNS servers, zones as routing domains, and a do-not-edit marker.
func TestGenerateZoneForwardConfig(t *testing.T) {
	got := generateZoneForwardConfig(
		[]string{"ruanjiangongcheng.site", "example.com"},
		[]string{"180.97.36.63", "183.232.231.249"},
	)
	for _, want := range []string{
		"[Resolve]",
		"DNS=180.97.36.63 183.232.231.249",
		"Domains=~example.com ~ruanjiangongcheng.site",
		"Managed by ssh-tunnel-manager",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected drop-in to contain %q, got:\n%s", want, got)
		}
	}

	// IP order must be normalized: resolver ordering noise must not
	// flip the status row between 已启用 and 有更新 across runs.
	shuffled := generateZoneForwardConfig(
		[]string{"example.com", "ruanjiangongcheng.site"},
		[]string{"183.232.231.249", "180.97.36.63"},
	)
	if shuffled != got {
		t.Errorf("expected deterministic output regardless of input order:\n%s\n---\n%s", got, shuffled)
	}
}

// TestZoneForwardStatusFile exercises the three install states against
// a redirected drop-in path: missing (off), matching (up to date), and
// diverging (needs update).
func TestZoneForwardStatusFile(t *testing.T) {
	expected := "DNS=1.2.3.4\nDomains=~z.example\n"

	old := resolvedDropInPath
	resolvedDropInPath = filepath.Join(t.TempDir(), "drop.conf")
	t.Cleanup(func() { resolvedDropInPath = old })

	// Missing file: feature off.
	if installed, _ := zoneForwardStatus(expected); installed {
		t.Error("expected not installed for missing file")
	}

	// Same content: installed and up to date.
	if err := os.WriteFile(resolvedDropInPath, []byte(expected), 0o644); err != nil {
		t.Fatal(err)
	}
	installed, upToDate := zoneForwardStatus(expected)
	if !installed || !upToDate {
		t.Errorf("expected installed+upToDate, got %v/%v", installed, upToDate)
	}

	// Different content (domain or NS changed): needs update.
	if err := os.WriteFile(resolvedDropInPath, []byte("DNS=5.6.7.8\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	installed, upToDate = zoneForwardStatus(expected)
	if !installed || upToDate {
		t.Errorf("expected installed but outdated, got %v/%v", installed, upToDate)
	}
}
