.PHONY: build build-linux build-windows build-deb install clean test smoke

# Build outputs go to build/ so the project root stays clean — only
# source files live at the top level.
BUILD_DIR := build

# Version is injected into the binary via -ldflags (shown in settings).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.appVersion=$(VERSION)

# Debian package version: dpkg requires the version to start with a
# digit, so a bare "dev" (no tags in the repo) degrades to 0.0.0.
DEB_VERSION := $(shell echo "$(VERSION)" | sed -e 's/^v//' | grep -E '^[0-9]' || echo 0.0.0)
DEB_ARCH ?= $(shell dpkg --print-architecture 2>/dev/null || echo amd64)

build:
	mkdir -p $(BUILD_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/ssh-tunnel-manager .
	go build -o $(BUILD_DIR)/querydns ./cmd/querydns

build-linux:
	mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/ssh-tunnel-manager-linux .
	GOOS=linux GOARCH=amd64 go build -o $(BUILD_DIR)/querydns-linux ./cmd/querydns

build-windows:
	mkdir -p $(BUILD_DIR)
	# -H=windowsgui: without it double-clicking the exe also opens a
	# console window alongside the GUI.
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS) -H=windowsgui" -o $(BUILD_DIR)/ssh-tunnel-manager.exe .
	GOOS=windows GOARCH=amd64 go build -o $(BUILD_DIR)/querydns.exe ./cmd/querydns

# build-deb: package the Linux binaries into a .deb so recipients can
# `sudo apt install ./ssh-tunnel-manager_...deb` — binary, menu entry
# and icon installed and removed through the package manager. The
# payload mirrors `make install` but uses /usr (the FHS location for
# packaged files; /usr/local is reserved for local admin installs).
# Override DEB_ARCH (amd64/arm64) when packaging for another machine.
build-deb: build
	rm -rf $(BUILD_DIR)/deb
	mkdir -p $(BUILD_DIR)/deb/DEBIAN \
	         $(BUILD_DIR)/deb/usr/bin \
	         $(BUILD_DIR)/deb/usr/share/applications \
	         $(BUILD_DIR)/deb/usr/share/icons/hicolor/128x128/apps
	sed -e 's/__VERSION__/$(DEB_VERSION)/' -e 's/__ARCH__/$(DEB_ARCH)/' \
	    packaging/debian-control.in > $(BUILD_DIR)/deb/DEBIAN/control
	install -Dm755 $(BUILD_DIR)/ssh-tunnel-manager $(BUILD_DIR)/deb/usr/bin/ssh-tunnel-manager
	install -Dm755 $(BUILD_DIR)/querydns $(BUILD_DIR)/deb/usr/bin/querydns
	install -Dm644 packaging/ssh-tunnel-manager.desktop $(BUILD_DIR)/deb/usr/share/applications/ssh-tunnel-manager.desktop
	install -Dm644 packaging/icon.png $(BUILD_DIR)/deb/usr/share/icons/hicolor/128x128/apps/ssh-tunnel-manager.png
	dpkg-deb --root-owner-group --build $(BUILD_DIR)/deb \
	    $(BUILD_DIR)/ssh-tunnel-manager_$(DEB_VERSION)_$(DEB_ARCH).deb

# install: system-wide install for desktop integration (binary on PATH,
# menu entry, hicolor icon). Override DESTDIR for staging/packaging.
DESTDIR ?=
PREFIX ?= /usr/local
install: build
	install -Dm755 $(BUILD_DIR)/ssh-tunnel-manager $(DESTDIR)$(PREFIX)/bin/ssh-tunnel-manager
	install -Dm755 $(BUILD_DIR)/querydns $(DESTDIR)$(PREFIX)/bin/querydns
	install -Dm644 packaging/ssh-tunnel-manager.desktop $(DESTDIR)$(PREFIX)/share/applications/ssh-tunnel-manager.desktop
	install -Dm644 packaging/icon.png $(DESTDIR)$(PREFIX)/share/icons/hicolor/128x128/apps/ssh-tunnel-manager.png

clean:
	rm -rf $(BUILD_DIR)

test:
	go test -v ./...

# smoke: end-to-end input-pipeline test — runs the real binary under
# Xvfb and clicks it with xdotool, so "renders but never delivers
# clicks" bugs (the zone-forward 启用 button) cannot ship again.
# Needs the xvfb, xdotool and x11-utils packages installed.
smoke: build
	./test/smoke.sh