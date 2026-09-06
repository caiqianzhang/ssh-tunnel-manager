# Gio patches carried by this project

This directory vendors gioui.org (upstream v0.10.2) so the app can
carry local fixes faster than an upstream release cycle. Every patch
is marked with a `Local patch:` comment at the change site.

| Patch | File(s) | Reason |
|---|---|---|
| Wayland clipboard-read wedge fix | `app/os_wayland.go` | `ReadClipboard()` did not reset `readClipClose` when no selection offer was pending, permanently disabling Ctrl+V after any too-early read. |
| X11 window actions (minimize / maximize / unmaximize) | `app/os_x11.go` `Perform()` | Upstream only wires Center/Raise/Close on X11. Custom-decorated windows need the rest. |
| X11 `_MOTIF_WM_HINTS` undecorate support | `app/os_x11.go` `Configure()` / `setMotifDecorations()` | Upstream forces `Decorated=true` on X11; the app draws its own title bar. |
| X11 interactive drag for undecorated windows | `app/os_x11.go` button/motion handling + `beginDrag()` | Honors `system.ActionMove` input ops; mutter ignores `_NET_WM_MOVERESIZE` for these windows, so the driver moves the window itself (`XMoveWindow`) and flushes. |
| `XFlush` after window-management XSendEvent calls | `app/os_x11.go` `raise()` / `setMotifDecorations()` | An iconified window produces no rendering frames, so nothing else flushed the output buffer — `_NET_ACTIVE_WINDOW` never reached the WM. |

## Upgrading Gio

Re-apply each patch above after replacing this directory with the new
upstream version, then run `go build ./...` and the manual checks in
the main README (title bar buttons, drag, undecorated window).
