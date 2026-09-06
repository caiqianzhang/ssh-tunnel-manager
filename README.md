# SSH Tunnel Manager

A small cross-platform desktop app that manages SSH local/remote port
forwards, written in Go with [Gio](https://gioui.org). Runs as a tray
application: the tray icon reflects the tunnel status and the menu
lets you connect, disconnect and restore the window.

![UI](docs/screenshot.png)

## Features

- Local (`-L`) and remote (`-R`) port forwarding over a plain `ssh`
  process, so existing known_hosts/agent settings keep working
- Status card with live tunnel state and forwarding address
- Port-conflict dialog (kill process / change port / ignore)
- Connection settings collapsed into the main page; config stored as
  JSON, SSH password encrypted (AES-256-GCM)
- Custom minimal title bar (minimize / maximize / close, draggable)
- Tray icon colored by status + live status and connect/disconnect
  menu; single-instance: launching it again restores the window
- Auto-reconnect with retry limits and process monitoring

## Build & run

Requirements: Go 1.24+, an SSH client in `PATH`, and `sshpass` if you
use password authentication (key-based auth also works).

```sh
make build            # Linux binary -> ./ssh-tunnel-manager
make build-windows    # Windows binary -> ./ssh-tunnel-manager.exe
make test
./ssh-tunnel-manager
```

## Configuration

Config lives in `~/.config/ssh-tunnel-manager/config.json`; the
encryption key for the stored SSH password is kept separately in
`~/.local/share/ssh-tunnel-manager/secret.key` so config backups do
not carry it. Both are migrated automatically from the legacy
executable-directory layout on first run. The log is written to the
per-user cache directory.

A template with placeholder values is committed as `config.example.json`.
Copy it to `config.json` (it is created automatically on first run) and
edit the values before connecting.

## Project layout

| Path | Contents |
|---|---|
| `main.go` | app lifecycle: single-instance, tray wiring, window loop |
| `ui.go` + `page_*.go`, `titlebar.go`, `theme.go`, `dialog_conflict.go` | Gio UI |
| `ssh.go` | tunnel process management, status transitions, reconnect |
| `config.go` / `crypto.go` / `logger.go` / `paths.go` | config persistence, password encryption, logging, file locations |
| `tray.go` | tray icon/menu (fyne.io/systray) |
| `third_party/gio` | vendored Gio with local patches — see `PATCHES.md` |
