PREFIX ?= $(HOME)/.local
BIN := $(PREFIX)/bin/oat
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test install

build:
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/oat ./cmd/oat

test:
	go test -race ./...

install: build
	install -Dm755 bin/oat $(BIN)
	$(BIN) setup
