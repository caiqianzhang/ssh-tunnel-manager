package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestQuitProcessExitsAndCleansUp pins the tray-quit contract: the
// process must actually terminate (app.Main blocks forever by design,
// so the old quit path left a zombie in the taskbar that also ate
// relaunch attempts) and remove the show-request socket.
//
// os.Exit never returns, so the assertion runs in a self-re-exec'd
// subprocess: the child calls quitProcess and must die with exit code 0
// before reaching the unreachable marker exit; the parent then checks
// the exit code and the socket file.
func TestQuitProcessExitsAndCleansUp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-only: uses the XDG_RUNTIME_DIR socket path")
	}

	// Redirect the socket path into a throwaway dir so the test never
	// touches a real instance's socket. The parent pre-creates a fake
	// socket file there; the child's quitProcess must delete it.
	tmp := t.TempDir()
	sock := filepath.Join(tmp, "ssh-tunnel-manager.sock")
	if err := os.WriteFile(sock, []byte("listener"), 0600); err != nil {
		t.Fatalf("create fake socket: %v", err)
	}

	if os.Getenv("STTM_TEST_QUIT_PROCESS") == "1" {
		// Child: reached only via the re-exec below.
		quitProcess() // must exit(0)
		os.Exit(7)    // unreachable if quitProcess works
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestQuitProcessExitsAndCleansUp$")
	// Duplicate env keys have unspecified precedence, so strip any
	// inherited XDG_RUNTIME_DIR before pointing it at our temp dir.
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "XDG_RUNTIME_DIR=") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env,
		"XDG_RUNTIME_DIR="+tmp,
		"STTM_TEST_QUIT_PROCESS=1",
	)

	err := cmd.Run()
	// A nil error means the child exited 0 — exactly what quitProcess
	// must do. Any error is a start failure or a non-zero exit: code 7
	// specifically means quitProcess returned and the marker was hit.
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 7 {
			t.Fatal("quitProcess returned without exiting; marker exit reached")
		}
		t.Fatalf("quitProcess did not exit cleanly: %v", err)
	}

	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("show-request socket still present after quitProcess: %v", err)
	}
}
