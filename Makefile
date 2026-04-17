.PHONY: fmt test build build-desktop check-desktop run run-desktop

fmt:
	gofmt -w $(shell go list -f '{{.Dir}}' ./...) ./internal/desktop/fyneapp ./cmd/hydrus-desktop

test:
	go test ./...

build:
	mkdir -p bin
	go build -o bin/hydrusd ./cmd/hydrusd

build-desktop:
	mkdir -p bin
	go build -tags fyne -o bin/hydrus-desktop ./cmd/hydrus-desktop

check-desktop:
	GOOS=js GOARCH=wasm go build -tags fyne ./cmd/hydrus-desktop

run:
	go run ./cmd/hydrusd

run-desktop:
	go run -tags fyne ./cmd/hydrus-desktop
