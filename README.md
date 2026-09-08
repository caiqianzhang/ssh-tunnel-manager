# SSH Tunnel Manager

[中文](README.zh.md) · A small cross-platform desktop app that manages SSH local/remote port
forwards, written in Go with [Gio](https://gioui.org). Runs as a tray
application: the tray icon reflects the tunnel status and the menu
lets you connect, disconnect and restore the window.

![UI](docs/screenshot.png)

## Features

- Local (`-L`) and remote (`-R`) port forwarding over a plain `ssh`
  process, so existing known_hosts/agent settings keep working
- DDNS fast-path on connect: with `baidu.key` present the server IP is
  pulled from the Baidu Cloud DNS API (authoritative, bypasses stale
  local resolver caches) and the OS DNS cache is flushed afterwards;
  otherwise the configured resolver (`settings.dns_resolver`, default
  `119.29.29.29`) is queried directly. Both paths hand the resolved IP
  to ssh itself, so the tunnel never depends on a second, possibly
  stale lookup through the local resolver. A heartbeat (default 15s,
  tunable via `settings.ddns_check_interval`) restarts the tunnel when
  the IP changes while it is up — with or without `baidu.key`
- Optional 域名解析优化 (Linux/desktop): one click in the settings page
  installs a systemd-resolved drop-in (through a pkexec password
  prompt) routing the forwarded domain straight to its authoritative
  nameservers, so local tools like ping/curl resolve fresh addresses
  instead of ISP-cached stale ones
- Status card with live tunnel state and forwarding address
- Port-conflict dialog (kill process / change port / ignore)
- Connection settings collapsed into the main page; config stored as
  JSON, SSH password encrypted (AES-256-GCM)
- Custom minimal title bar (minimize / maximize / close, draggable)
- Tray icon colored by status + live status and connect/disconnect
  menu; single-instance: launching it again restores the window
- Auto-reconnect with retry limits and process monitoring

## Build & run

Requirements: Go 1.26+, an SSH client in `PATH`, and `sshpass` if you
use password authentication (key-based auth also works).

On a clean Linux box, Gio's CGO backend also needs the graphics dev
headers (Ubuntu/Debian package names):

```sh
sudo apt install build-essential pkg-config \
    libx11-dev libxkbcommon-dev libxkbcommon-x11-dev \
    libwayland-dev libegl1-mesa-dev libgles2-mesa-dev
```

```sh
make build            # Linux binary -> build/ssh-tunnel-manager
make build-windows    # Windows binary -> build/ssh-tunnel-manager.exe
make test
./build/ssh-tunnel-manager
```

The Windows build is best-effort: single-instance enforcement,
port-conflict process identification/killing, and the DNS
zone-forwarding feature all rely on Linux tooling (flock/unix
sockets, ss/fuser/lsof, pkexec + resolvectl) that is unavailable or
degraded on Windows.

The build stamps the version (git describe) into the binary; the
settings page shows it. Optional runtime helpers are auto-detected:
`pkexec` + `resolvectl` power the 域名解析优化 row in settings.

### Desktop integration (optional, Linux)

```sh
sudo make install     # binary to /usr/local/bin + menu entry + icon
```

Afterwards the app starts from the desktop menu with its own icon.
Uninstall by removing `/usr/local/bin/ssh-tunnel-manager`,
`/usr/local/bin/querydns`, and the ssh-tunnel-manager entries under
`/usr/local/share/applications` and `/usr/local/share/icons`.

## Configuration

The app manages exactly ONE forwarding rule. Config lives in
`~/.config/ssh-tunnel-manager/config.json`; the
encryption key for the stored SSH password is kept separately in
`~/.local/share/ssh-tunnel-manager/secret.key` so config backups do
not carry it. Both are migrated automatically from the legacy
executable-directory layout on first run. The log is written to the
per-user cache directory. A legacy (multi-forward list) `config.json`
is migrated on load: the first entry becomes the app's forward, the
rest are dropped.

A template with placeholder values is committed as `config.example.json`.
Copy it to `config.json` (it is created automatically on first run) and
edit the values before connecting. `settings.api_key` is the Anthropic
API key sent through the tunnel by the in-app 测试 button (optional).
Note: an empty SSH password field means "keep the stored password", so
a saved password cannot be cleared from the UI — edit or remove the
field in `config.json` instead.

When `baidu.key` is present the app uses the Baidu Cloud DNS API to
resolve the forwarded hostname; when it is absent it falls back to the
DNS server in `settings.dns_resolver` (default `119.29.29.29`, Baidu's
public DNS) instead of the local system resolver, because for a DDNS
host the local cache is exactly the stale record we are trying to
avoid. Set `settings.dns_resolver` to an empty string to use the system
resolver.

## Project layout

| Path | Contents |
|---|---|
| `main.go` | app lifecycle: single-instance, tray wiring, window loop |
| `ui.go` + `page_*.go`, `titlebar.go`, `theme.go`, `dialog_conflict.go` | Gio UI |
| `ssh.go` | tunnel process management, status transitions, reconnect |
| `config.go` / `crypto.go` / `logger.go` / `paths.go` | config persistence, password encryption, logging, file locations |
| `tray.go` | tray icon/menu (fyne.io/systray) |
| `cmd/querydns` | standalone CLI: dump Baidu Cloud DNS records for a zone (reads `baidu.key`) |
| `third_party/gio` | vendored Gio with local patches — see `PATCHES.md` |
