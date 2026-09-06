.PHONY: build build-linux build-windows install clean test

# Build outputs go to build/ so the project root stays clean — only
# source files live at the top level.
BUILD_DIR := build

# Version is injected into the binary via -ldflags (shown in settings).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.appVersion=$(VERSION)

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
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/ssh-tunnel-manager.exe .
	GOOS=windows GOARCH=amd64 go build -o $(BUILD_DIR)/querydns.exe ./cmd/querydns

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