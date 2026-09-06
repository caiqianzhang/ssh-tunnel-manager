//go:build windows

package main

import "os"

// Windows has no flock-equivalent wired up here yet; single-instance
// enforcement degrades to a no-op rather than block the build.
func acquireSingleInstanceLock() (*os.File, error) {
	return nil, nil
}

// notifyRunningInstance reports that no existing instance could be
// reached (the check is only implemented on Unix).
func notifyRunningInstance() bool {
	return false
}

// listenForShowRequests has no Unix-socket counterpart on Windows.
func listenForShowRequests(showCh chan struct{}) {
}
