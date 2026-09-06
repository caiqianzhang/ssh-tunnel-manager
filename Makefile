.PHONY: build build-linux build-windows clean test

# Build outputs go to build/ so the project root stays clean — only
# source files live at the top level.
BUILD_DIR := build

build:
	mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/ssh-tunnel-manager .

build-linux:
	mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 go build -o $(BUILD_DIR)/ssh-tunnel-manager-linux .

build-windows:
	mkdir -p $(BUILD_DIR)
	GOOS=windows GOARCH=amd64 go build -o $(BUILD_DIR)/ssh-tunnel-manager.exe .

clean:
	rm -rf $(BUILD_DIR)

test:
	go test -v ./...