package main

import (
	"net"
	"testing"
	"time"
)

// listenFreePort binds a TCP listener on port 0 (kernel-assigned), records
// the port, then immediately closes it. Returns the chosen port.
// Returns 0 on failure (test should skip in that case).
func listenFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Logf("listenFreePort failed: %v", err)
		return 0
	}
	defer l.Close()
	addr := l.Addr().(*net.TCPAddr)
	return addr.Port
}

// nowMillis returns current wall time in milliseconds (int64).
// Uses time.Now so we don't introduce flaky time.Time arithmetic.
func nowMillis() int64 {
	return time.Now().UnixNano() / int64(time.Millisecond)
}
