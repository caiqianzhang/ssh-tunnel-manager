package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRotateLogIfNeeded covers the size cap: a log at/over the limit is
// renamed to <path>.1 (replacing any previous generation), a small log
// is left alone.
func TestRotateLogIfNeeded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ssh-tunnel-manager.log")
	big := strings.Repeat("x", maxLogSize)

	// Over the limit: rotates, replacing the previous .1 generation.
	if err := os.WriteFile(path, []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".1", []byte("old-generation"), 0o600); err != nil {
		t.Fatal(err)
	}
	rotateLogIfNeeded(path)
	got, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("rotated generation missing: %v", err)
	}
	if len(got) != maxLogSize {
		t.Errorf("rotated file size = %d, want %d", len(got), maxLogSize)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("original log still present after rotation")
	}

	// Under the limit: untouched.
	if err := os.WriteFile(path, []byte("small"), 0o600); err != nil {
		t.Fatal(err)
	}
	rotateLogIfNeeded(path)
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "small" {
		t.Errorf("small log was rotated/modified: %v %q", err, data)
	}
}
