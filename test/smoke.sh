#!/usr/bin/env bash
# End-to-end input-pipeline smoke test.
#
# Runs the real binary on a virtual X server (Xvfb) and drives it with
# synthetic mouse clicks (xdotool), asserting each click produced its
# log-line effect. This exists because a Gio button can render
# perfectly while never delivering clicks (missing widget.Layout hit
# area — the zone-forward 启用 bug): no unit test or vet pass can see
# it, only a real pointer event through a real window can.
#
# Requirements: Xvfb, xdotool, x11-utils (xdpyinfo). CI installs them
# in .github/workflows/test.yml; locally run `make smoke`.

set -u
cd "$(dirname "$0")/.."

BIN=./build/ssh-tunnel-manager
[ -x "$BIN" ] || { echo "FAIL: $BIN not built (run make build first)"; exit 1; }
command -v Xvfb >/dev/null || { echo "FAIL: Xvfb not installed"; exit 1; }
command -v xdotool >/dev/null || { echo "FAIL: xdotool not installed"; exit 1; }

TMP=$(mktemp -d)
XVFB_PID=""
APP_PID=""
cleanup() {
    [ -n "$APP_PID" ] && kill "$APP_PID" 2>/dev/null
    [ -n "$XVFB_PID" ] && kill "$XVFB_PID" 2>/dev/null
    rm -rf "$TMP"
}
trap cleanup EXIT

# Isolated XDG dirs: the app must never touch the developer's real
# config/cache, and the log lands where we can read it.
export XDG_CONFIG_HOME="$TMP/config" XDG_CACHE_HOME="$TMP/cache"
# Empty runtime dir: without it Gio would ignore DISPLAY and connect
# to the real session via the default Wayland socket
# ($XDG_RUNTIME_DIR/wayland-0), and the smoke test would click the
# developer's actual app window instead of the isolated one.
export XDG_RUNTIME_DIR="$TMP/runtime"
mkdir -p "$XDG_RUNTIME_DIR"
unset DBUS_SESSION_BUS_ADDRESS
mkdir -p "$XDG_CONFIG_HOME" "$XDG_CACHE_HOME"
LOG="$XDG_CACHE_HOME/ssh-tunnel-manager/ssh-tunnel-manager.log"

# 1. Virtual display. 640x480 fits the 280x420 app window; scale is 1,
#    so dp coordinates equal pixels.
Xvfb :97 -screen 0 640x480x24 >/dev/null 2>&1 &
XVFB_PID=$!
for _ in $(seq 1 50); do
    xdpyinfo -display :97 >/dev/null 2>&1 && break
    sleep 0.1
done
xdpyinfo -display :97 >/dev/null 2>&1 || { echo "FAIL: Xvfb did not start"; exit 1; }
unset WAYLAND_DISPLAY DISPLAY
export DISPLAY=:97

# 2. Launch the app.
"$BIN" >/dev/null 2>&1 &
APP_PID=$!

# 3. Wait for the window to appear.
WIN=""
for _ in $(seq 1 100); do
    WIN=$(xdotool search --onlyvisible --class "ssh-tunnel-manager" 2>/dev/null | head -1)
    [ -n "$WIN" ] && break
    sleep 0.2
done
[ -n "$WIN" ] || { echo "FAIL: app window not found"; exit 1; }
xdotool windowactivate --sync "$WIN" >/dev/null 2>&1
sleep 1

# click_and_expect clicks at (x,y) relative to the window, then polls
# the log until `pattern` gains a new line. Probing a few y values
# absorbs layout drift (padding changes etc.) without pinning exact
# pixel offsets.
click_and_expect() {
    local x=$1 shift_y=$2 pattern=$3 label=$4
    local before after y
    before=$(grep -c "$pattern" "$LOG" 2>/dev/null || true)
    before=${before:-0}
    for y in $shift_y; do
        xdotool mousemove --window "$WIN" "$x" "$y" click 1
        for _ in $(seq 1 20); do
            sleep 0.2
            after=$(grep -c "$pattern" "$LOG" 2>/dev/null || true)
            after=${after:-0}
            if [ "$after" -gt "$before" ]; then
                echo "OK: $label (y=$y)"
                return 0
            fi
        done
    done
    echo "FAIL: $label — clicked ($x, $shift_y), no new '$pattern' in log"
    exit 1
}

# 4. 连接 button: left half of the action row, near the bottom of the
#    compact layout. Any click logs "toggle: connect button clicked"
#    before any validation runs, so an empty config is fine.
click_and_expect 70 "335 345 355 365" "toggle: connect button clicked" "连接 button delivers clicks"

# 5. Settings header: bottom strip of the page.
click_and_expect 140 "395 405 415" "settings: header clicked" "设置 header delivers clicks"

# 6. Titlebar close: top-right, fixed geometry. The destroy event
#    proves the whole chain (click → widget → window action) works.
click_and_expect 264 "16" "window destroyed event received" "titlebar 关闭 button"

# 7. App must survive window close (it waits in the tray).
kill -0 "$APP_PID" 2>/dev/null || { echo "FAIL: app exited after window close"; exit 1; }

echo "SMOKE TEST PASSED"
