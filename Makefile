VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	go build -ldflags "-s -w -X main.version=$(VERSION)" -o courier ./cmd/courier

test:
	go vet ./...
	go test -race ./...

# Needs fm serve running on 127.0.0.1:1976.
test-fm:
	COURIER_FM_TEST=1 go test -run TestRealFM -v ./internal/bot

install: build
	install -m 0755 courier $(HOME)/.local/bin/courier

.PHONY: build test test-fm install
