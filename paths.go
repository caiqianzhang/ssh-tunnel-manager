package main

// Central location for every file the app writes, following the XDG
// Base Directory specification (os.UserConfigDir / os.UserCacheDir /
// XDG_STATE_HOME).
//
// Legacy layout: early builds kept config.json, secret.key and the
// log next to the executable / in the working directory. migrateFile
// moves such files into the new locations on first run and removes
// the legacy copy after a successful move (the old config.json held
// plaintext passwords, so keeping it around is a security leak).

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

// knownHostsFile returns the path of the per-user SSH known-hosts
// file. StrictHostKeyChecking=accept-new refuses to connect to a host
// whose key has changed, so the file must persist between runs.
func knownHostsFile() (string, error) {
	dir, err := appConfigDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "known_hosts"), nil
}

// knownHostsPath is the non-error-returning variant used by
// buildSSHCommand, which runs outside a context that can propagate
// errors. An empty string means "use ssh's default" — which is fine
// because the user's own ~/.ssh/known_hosts is still consulted.
func knownHostsPath() string {
	p, err := knownHostsFile()
	if err != nil {
		return ""
	}
	return p
}

// migrateFile copies src to dst when src exists and dst does not.
// src is resolved relative to the executable's directory so the
// migration works regardless of the user's current working directory.
//
// The legacy file is removed after a successful copy: early builds
// stored the SSH password in plaintext in config.json and the AES
// key as secret.key next to the executable. Leaving those copies
// behind defeats the point of moving them to 0700/0600-permissioned
// locations.
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

	// Remove the legacy plaintext copy. Best-effort: if the executable
	// directory is not writable (e.g. a system location) the removal
	// fails harmlessly and the old file is left as a backup.
	if err := os.Remove(src); err != nil {
		Logf("migrate: failed to remove legacy %s: %v", src, err)
	}
}
