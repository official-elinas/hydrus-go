.PHONY: fmt test build build-desktop build-desktop-linux build-desktop-windows check-desktop run run-lan run-desktop

LAN_LISTEN_ADDR ?= 0.0.0.0:45869
LINUX_GOARCH ?= amd64
LINUX_CC ?= gcc
WINDOWS_GOARCH ?= amd64
WINDOWS_CC ?= x86_64-w64-mingw32-gcc

fmt:
	gofmt -w $(shell go list -f '{{.Dir}}' ./...) ./internal/desktop/fyneapp ./cmd/hydrus-desktop

test:
	go test ./...

build:
	mkdir -p bin
	go build -o bin/hydrusd ./cmd/hydrusd

build-cgo:
	mkdir -p bin
	CGO_ENABLED=1 CC=/usr/bin/gcc GOOS=linux go build -o bin/hydrusd ./cmd/hydrusd

build-desktop:
	mkdir -p bin
	go build -tags fyne -o bin/hydrus-desktop ./cmd/hydrus-desktop

build-desktop-linux:
	mkdir -p bin
	CGO_ENABLED=1 CC=$(LINUX_CC) GOOS=linux GOARCH=$(LINUX_GOARCH) go build -tags fyne -o bin/hydrus-desktop ./cmd/hydrus-desktop

build-desktop-windows:
	mkdir -p bin
	CGO_ENABLED=1 CC=$(WINDOWS_CC) GOOS=windows GOARCH=$(WINDOWS_GOARCH) go build -tags fyne -ldflags="-H windowsgui" -o bin/hydrus-desktop.exe ./cmd/hydrus-desktop

check-desktop:
	mkdir -p bin
	GOOS=js GOARCH=wasm go build -tags fyne -o bin/hydrus-desktop.wasm ./cmd/hydrus-desktop

run:
	go run ./cmd/hydrusd

run-lan:
	go run ./cmd/hydrusd --listen $(LAN_LISTEN_ADDR)

run-desktop:
	go run -tags fyne ./cmd/hydrus-desktop
