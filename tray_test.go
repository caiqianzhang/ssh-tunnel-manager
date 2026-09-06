package main

import (
	"testing"
	"time"
)

// Bug 23/24: signalQuit must be non-blocking and idempotent so that
// repeated calls from systray cannot deadlock.
func TestSignalQuitIsNonBlockingAndIdempotent(t *testing.T) {
	ch := make(chan struct{}, 1)

	// First send should succeed.
	signalQuit(ch)
	if len(ch) != 1 {
		t.Errorf("after first signalQuit, channel len=%d, want 1", len(ch))
	}

	// Second send should NOT block (the channel is full). Run it in a
	// goroutine and time it out.
	done := make(chan struct{})
	go func() {
		signalQuit(ch)
		close(done)
	}()
	select {
	case <-done:
		// Good — second call returned immediately.
	case <-time.After(100 * time.Millisecond):
		t.Fatal("second signalQuit blocked (should be non-blocking)")
	}
}

func TestSignalQuitWakesConsumer(t *testing.T) {
	ch := make(chan struct{}, 1)
	got := make(chan struct{})
	go func() {
		<-ch
		close(got)
	}()
	signalQuit(ch)
	select {
	case <-got:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("signalQuit did not wake consumer")
	}
}
