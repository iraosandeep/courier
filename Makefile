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

PLATFORMS = darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64

# Cross-compile every release platform into dist/ (CI uses this as a build check).
cross:
	@rm -rf dist && mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=""; [ $$os = windows ] && ext=".exe"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" \
			-o dist/courier_$${os}_$${arch}$$ext ./cmd/courier || exit 1; \
	done

.PHONY: build test test-fm install cross
