package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// Logger provides a simple file-based logging facility.
type Logger struct {
	mu      sync.Mutex
	file    *os.File
	logPath string
}

var globalLogger *Logger

// maxLogSize is the size at which the log rotates to <path>.1,
// overwriting the previous generation. A tray app runs for weeks; an
// unbounded append-only log would grow forever.
const maxLogSize = 5 << 20 // 5 MiB

// rotateLogIfNeeded renames the current log to <path>.1 if it grew past
// maxLogSize. Called before opening the log for a new session, so at
// most two files (current + .1) ever exist.
func rotateLogIfNeeded(path string) {
	if info, err := os.Stat(path); err == nil && info.Size() >= maxLogSize {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
}

// InitLogger initializes the global logger, writing to a file in the user's config directory.
// Returns the log file path on success.
func InitLogger() (string, error) {
	logPath, err := getLogPath()
	if err != nil {
		return "", err
	}

	rotateLogIfNeeded(logPath)

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return "", fmt.Errorf("failed to open log file %s: %v", logPath, err)
	}

	globalLogger = &Logger{
		file:    f,
		logPath: logPath,
	}

	Log("=== SSH Tunnel Manager started ===")
	Log(fmt.Sprintf("OS: %s, Arch: %s", runtime.GOOS, runtime.GOARCH))
	Log(fmt.Sprintf("Log file: %s", logPath))

	return logPath, nil
}

// getLogPath returns the path to the log file (per-user cache dir —
// the log is regenerable state, not config or data).
func getLogPath() (string, error) {
	dir, err := appCacheDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "ssh-tunnel-manager.log"), nil
}

// Log writes a message to the log file with timestamp.
func Log(msg string) {
	if globalLogger == nil {
		return
	}
	globalLogger.mu.Lock()
	defer globalLogger.mu.Unlock()

	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	line := fmt.Sprintf("[%s] %s\n", timestamp, msg)
	if _, err := globalLogger.file.WriteString(line); err != nil {
		fmt.Fprintf(os.Stderr, "log write error: %v\n", err)
	}
}

// Logf writes a formatted message to the log file.
func Logf(format string, args ...interface{}) {
	Log(fmt.Sprintf(format, args...))
}

// CloseLogger closes the log file.
func CloseLogger() {
	if globalLogger == nil {
		return
	}
	globalLogger.mu.Lock()
	defer globalLogger.mu.Unlock()
	if globalLogger.file != nil {
		globalLogger.file.Close()
	}
}

// GetLogPath returns the current log file path.
func GetLogPath() string {
	if globalLogger == nil {
		return ""
	}
	return globalLogger.logPath
}
