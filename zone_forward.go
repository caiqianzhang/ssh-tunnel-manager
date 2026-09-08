package main

// Zone-forwarding integration (Linux + systemd-resolved only).
//
// Running the app on a fresh machine means its local resolver path
// suffers the same stale-cache problem we hit in development: ISP
// recursive servers ignore low DDNS TTLs and serve dead addresses for
// hours. The fix is a tiny systemd-resolved drop-in that routes queries
// for the forwarded domain straight to its authoritative nameservers.
//
// The app manages that drop-in itself: the settings page shows a status
// row, and installing/removing goes through pkexec so the user gets the
// desktop's graphical authentication dialog — no terminal, no
// pre-shared sudoers entry.

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// resolvedDropInPath is the drop-in file systemd-resolved merges into
// its configuration on startup. NOTE: the directory is resolved.conf.d
// — the sibling resolved.d is NOT read by systemd. A var so tests can
// redirect it.
var resolvedDropInPath = "/etc/systemd/resolved.conf.d/ssh-tunnel-manager.conf"

// Actions the settings button can request.
const (
	zoneActionInstall = "install"
	zoneActionRemove  = "remove"
)

// zoneForwardSupported reports whether the host can use the feature at
// all: Linux with systemd-resolved reachable via resolvectl.
func zoneForwardSupported() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	if _, err := exec.LookPath("resolvectl"); err != nil {
		return false
	}
	if _, err := os.Stat("/run/systemd/resolve"); err != nil {
		return false
	}
	return true
}

// lookupZoneNSIPs resolves the authoritative nameservers of zone to IP
// addresses (NS records → A/AAAA). Any single unresolvable NS host is
// skipped; only a total failure is an error.
func lookupZoneNSIPs(zone string) ([]string, error) {
	nss, err := net.LookupNS(zone)
	if err != nil {
		return nil, fmt.Errorf("查询 %s 的 NS 记录失败: %w", zone, err)
	}
	var ips []string
	seen := make(map[string]bool)
	for _, ns := range nss {
		addrs, err := net.LookupHost(ns.Host)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if !seen[a] {
				seen[a] = true
				ips = append(ips, a)
			}
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("未能解析 %s 权威 NS 的地址", zone)
	}
	return ips, nil
}

// expectedZoneForwardContent renders the drop-in content for zone.
func expectedZoneForwardContent(zone string) (string, error) {
	ips, err := lookupZoneNSIPs(zone)
	if err != nil {
		return "", err
	}
	return generateZoneForwardConfig([]string{zone}, ips), nil
}

// generateZoneForwardConfig renders the drop-in: the NS IPs become the
// DNS servers, the zones become routing domains (~ prefix). IP order is
// normalized (sorted) so two runs against the same zone always render
// byte-identical content — zoneForwardStatus relies on that to detect
// real drift instead of resolver ordering noise. Pure function; tests
// pin the output.
func generateZoneForwardConfig(zones, dnsIPs []string) string {
	var b strings.Builder
	b.WriteString("# Managed by ssh-tunnel-manager — do not edit.\n")
	b.WriteString("# Routes the tunneled domain straight to its authoritative\n")
	b.WriteString("# nameservers, bypassing recursive caches that serve stale\n")
	b.WriteString("# records for low-TTL DDNS names.\n")
	b.WriteString("[Resolve]\n")
	ips := append([]string(nil), dnsIPs...)
	sort.Strings(ips)
	b.WriteString("DNS=" + strings.Join(ips, " ") + "\n")
	zs := append([]string(nil), zones...)
	sort.Strings(zs)
	doms := make([]string, len(zs))
	for i, z := range zs {
		doms[i] = "~" + z
	}
	b.WriteString("Domains=" + strings.Join(doms, " ") + "\n")
	return b.String()
}

// zoneForwardStatus compares the on-disk drop-in against the expected
// content: installed=false means the file is absent (feature off);
// installed && !upToDate means it exists but the forward's domain or
// NS set changed since it was written.
func zoneForwardStatus(expected string) (installed, upToDate bool) {
	data, err := os.ReadFile(resolvedDropInPath)
	if err != nil {
		return false, false
	}
	return true, strings.TrimSpace(string(data)) == strings.TrimSpace(expected)
}

// applyZoneForward installs or refreshes the drop-in: the generated
// content is staged in a user-writable temp file, then a root helper
// (via pkexec) installs it and restarts systemd-resolved.
func applyZoneForward(content string) error {
	tmp, err := os.CreateTemp("", "sttm-zone-*.conf")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("设置临时文件权限失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}

	script := fmt.Sprintf("mkdir -p %q && install -m 644 %q %q && systemctl restart systemd-resolved",
		filepath.Dir(resolvedDropInPath), tmpName, resolvedDropInPath)
	return runPolkitScript(script)
}

// removeZoneForward deletes the drop-in and restarts systemd-resolved.
func removeZoneForward() error {
	script := fmt.Sprintf("rm -f %q && systemctl restart systemd-resolved", resolvedDropInPath)
	return runPolkitScript(script)
}

// runPolkitScript runs script as root through pkexec, which shows the
// desktop's graphical authentication dialog (a cancelled dialog fails
// with a non-zero exit). No terminal and no pre-shared credentials
// needed on standard desktop Linux.
func runPolkitScript(script string) error {
	if _, err := exec.LookPath("pkexec"); err != nil {
		return fmt.Errorf("未找到图形授权工具 pkexec，请手动执行: %s", script)
	}
	// Generous window: the pkexec dialog may wait for the user to come
	// back to the machine and type their password; 2 minutes used to
	// abort a perfectly valid authentication attempt.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "pkexec", "sh", "-c", script).CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("授权超时")
		}
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%v: %s", err, msg)
		}
		return err
	}
	return nil
}
