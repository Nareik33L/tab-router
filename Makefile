GO ?= go
BIN ?= dist
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo v0.1.0)
LDFLAGS := -s -w -X github.com/Nareik33L/tab-router/controller/startup.Version=$(VERSION)

.PHONY: all build build-all test test-unit test-isolation vet fmt fetch-chromium infra clean

all: vet build

build:
	$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN)/tab-router ./cmd/tab-router

# Product targets: Windows and macOS. Linux is a dev/CI host only.
# Stable names (tab-router-mac-apple-silicon, etc.) are what
# …/releases/latest/download/… serves, so the README never needs a version number.
build-all:
	mkdir -p $(BIN)
	GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN)/tab-router-$(VERSION)-windows-amd64.exe ./cmd/tab-router
	GOOS=darwin  GOARCH=arm64 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN)/tab-router-$(VERSION)-darwin-arm64 ./cmd/tab-router
	GOOS=darwin  GOARCH=amd64 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN)/tab-router-$(VERSION)-darwin-amd64 ./cmd/tab-router
	cp $(BIN)/tab-router-$(VERSION)-windows-amd64.exe $(BIN)/tab-router-windows.exe
	cp $(BIN)/tab-router-$(VERSION)-darwin-arm64 $(BIN)/tab-router-mac-apple-silicon
	cp $(BIN)/tab-router-$(VERSION)-darwin-amd64 $(BIN)/tab-router-mac-intel

vet:
	$(GO) vet ./...
	GOOS=windows GOARCH=amd64 $(GO) vet ./...
	GOOS=darwin  GOARCH=arm64 $(GO) vet ./...

fmt:
	gofmt -l -w .

# Unit tests need no browser.
test-unit:
	$(GO) test ./controller/... ./routing/... ./ipc/... ./tests/routing/... -count=1

# Isolation and browser suites need a Chromium; set TAB_ROUTER_CHROMIUM or
# run `make fetch-chromium` first.
test-isolation:
	TAB_ROUTER_REQUIRE_CHROMIUM=1 $(GO) test ./tests/browser/... ./tests/isolation/... -count=1 -v

test: test-unit test-isolation

fetch-chromium:
	$(GO) run ./scripts/fetch-chromium

infra:
	$(GO) run ./tests/infra/cmd/tab-router-infra --out ./data

clean:
	rm -rf $(BIN) bin
