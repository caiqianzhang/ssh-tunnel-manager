package main

// Central location for every file the app writes, following the XDG
// Base Directory specification (os.UserConfigDir / os.UserCacheDir /
// XDG_STATE_HOME).
//
// Legacy layout: early builds kept config.json, secret.key and the
// log next to the executable / in the working directory. migrateFile
// moves such files into the new locations on first run; the original
// files are left untouched as a backup.

import (
	"io"
	"os"
	"path/filepath"
)

// appConfigDir returns ~/.config/ssh-tunnel-manager (platform-equivalent).
func appConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "ssh-tunnel-manager"), nil
}

// appDataDir returns the per-user data directory (XDG_DATA_HOME or
// ~/.local/share); used for the secret key, which must NOT travel
// with config backups. On Windows this falls back to the config dir.
func appDataDir() (string, error) {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return appConfigDir()
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "ssh-tunnel-manager"), nil
}

// appCacheDir returns the directory for regenerable state (the log).
func appCacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "ssh-tunnel-manager"), nil
}

// configFile returns the path of the tunnel configuration file,
// migrating a legacy config.json from the working directory if needed.
func configFile() (string, error) {
	dir, err := appConfigDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, "config.json")
	if _, err := os.Stat(dst); os.IsNotExist(err) {
		migrateFile("config.json", dst)
	}
	return dst, nil
}

// secretKeyFile returns the path of the AES key file, migrating a
// legacy secret.key from the working directory if needed.
func secretKeyFile() (string, error) {
	dir, err := appDataDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, "secret.key")
	if _, err := os.Stat(dst); os.IsNotExist(err) {
		migrateFile("secret.key", dst)
	}
	return dst, nil
}

// migrateFile copies src to dst when src exists and dst does not.
// src is resolved relative to the executable's directory so the
// migration works regardless of the user's current working directory.
func migrateFile(src, dst string) {
	exe, err := os.Executable()
	if err != nil {
		Logf("migrate: cannot resolve executable path: %v", err)
		return
	}
	src = filepath.Join(filepath.Dir(exe), src)

	in, err := os.Open(src)
	if err != nil {
		return // nothing to migrate
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		Logf("migrate: %s -> %s failed: %v", src, dst, err)
		return
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		Logf("migrate: copying %s -> %s failed: %v", src, dst, err)
		return
	}
	Logf("migrated legacy file %s -> %s", src, dst)
}
