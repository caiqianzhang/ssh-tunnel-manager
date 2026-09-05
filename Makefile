.PHONY: build build-linux build-windows clean test

build:
	go build -o ssh-tunnel-manager .

build-linux:
	GOOS=linux GOARCH=amd64 go build -o ssh-tunnel-manager-linux .

build-windows:
	GOOS=windows GOARCH=amd64 go build -o ssh-tunnel-manager.exe .

clean:
	rm -f ssh-tunnel-manager ssh-tunnel-manager-linux ssh-tunnel-manager.exe

test:
	go test -v ./...
