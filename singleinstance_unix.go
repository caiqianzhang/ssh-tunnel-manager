//go:build !windows

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// acquireSingleInstanceLock takes an exclusive flock on a lock file so
// only one app instance runs at a time. Without it a second launch
// silently fights the first over the same local forward port and pops
// a port-conflict dialog against the app's own tunnel. The returned
// file must stay open for the process lifetime (closing it releases
// the lock); a stale file after a crash is harmless because flock is
// released automatically by the OS.
//
// The lock lives in the user's config directory (0700), NOT /tmp: on
// multi-user systems /tmp is shared, so a second user's launch would
// collide with the first user's lock (or vice versa).
func acquireSingleInstanceLock() (*os.File, error) {
	dir, err := appConfigDir()
	if err != nil {
		return nil, fmt.Errorf("resolve config dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "ssh-tunnel-manager.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("cannot open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("SSH Tunnel Manager 已经在运行")
	}
	return f, nil
}

// runtimeSocketPath returns the path of the local socket used by a
// second launch to poke the first instance. Prefers XDG_RUNTIME_DIR
// (per-user, world-writable) and falls back to the user's config dir
// rather than a shared /tmp path.
func runtimeSocketPath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "ssh-tunnel-manager.sock")
	}
	dir, err := appConfigDir()
	if err == nil {
		return filepath.Join(dir, "ssh-tunnel-manager.sock")
	}
	return filepath.Join(os.TempDir(), "ssh-tunnel-manager.sock")
}

// notifyRunningInstance asks an already-running instance to show its
// window. Returns true if a running instance was reached.
func notifyRunningInstance() bool {
	conn, err := net.Dial("unix", runtimeSocketPath())
	if err != nil {
		return false
	}
	defer conn.Close()
	fmt.Fprintln(conn, "show")
	return true
}

// listenForShowRequests listens on the local socket and turns every
// connection from a second launch into a show request on showCh.
func listenForShowRequests(showCh chan struct{}) {
	path := runtimeSocketPath()
	os.Remove(path) // stale socket from a crashed instance
	ln, err := net.Listen("unix", path)
	if err != nil {
		Logf("show-request listener: %v", err)
		return
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				// A transient error (e.g. EMFILE under load) must
				// not kill the listener — a second launch would then
				// fail to wake this instance and start a competing
				// process. Retry after a brief pause. The listener
				// is never closed in this process, so the only fatal
				// case is the listener being closed, which is also
				// harmless to retry.
				Logf("show-request listener: Accept error: %v", err)
				time.Sleep(100 * time.Millisecond)
				continue
			}
			select {
			case showCh <- struct{}{}:
			default:
			}
			conn.Close()
		}
	}()
}
