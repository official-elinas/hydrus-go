.PHONY: fmt test build run

fmt:
	gofmt -w $(shell go list -f '{{.Dir}}' ./...)

test:
	go test ./...

build:
	mkdir -p bin
	go build -o bin/hydrusd ./cmd/hydrusd

run:
	go run ./cmd/hydrusd
