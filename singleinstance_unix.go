//go:build !windows

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

// acquireSingleInstanceLock takes an exclusive flock on a lock file so
// only one app instance runs at a time. Without it a second launch
// silently fights the first over the same local forward port and pops
// a port-conflict dialog against the app's own tunnel. The returned
// file must stay open for the process lifetime (closing it releases
// the lock); a stale file after a crash is harmless because flock is
// released automatically by the OS.
func acquireSingleInstanceLock() (*os.File, error) {
	f, err := os.OpenFile("/tmp/ssh-tunnel-manager.lock", os.O_CREATE|os.O_RDWR, 0600)
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
// second launch to poke the first instance.
func runtimeSocketPath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
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
				return
			}
			select {
			case showCh <- struct{}{}:
			default:
			}
			conn.Close()
		}
	}()
}
