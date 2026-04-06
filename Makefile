VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.Version=$(VERSION)

.DEFAULT_GOAL := build

## Build the databricks-codex binary
build:
	go build -ldflags="$(LDFLAGS)" -o databricks-codex .

## Install to GOPATH/bin
install:
	go install -ldflags="$(LDFLAGS)" .

## Run tests with verbose output
test:
	go test ./... -v

## Cross-compile for linux/darwin/windows amd64 + arm64
dist:
	mkdir -p dist
	GOOS=darwin  GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o dist/databricks-codex-darwin-arm64  .
	GOOS=darwin  GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o dist/databricks-codex-darwin-amd64  .
	GOOS=linux   GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o dist/databricks-codex-linux-amd64   .
	GOOS=linux   GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o dist/databricks-codex-linux-arm64   .
	GOOS=windows GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o dist/databricks-codex-windows-amd64.exe .
	GOOS=windows GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o dist/databricks-codex-windows-arm64.exe .

## Remove build artifacts
clean:
	rm -f databricks-codex
	rm -rf dist/

## Run go vet
lint:
	go vet ./...

.PHONY: build install test dist clean lint
