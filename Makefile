GO ?= go
BIN ?= bin

.PHONY: all build build-all test test-unit test-isolation vet fmt fetch-chromium infra clean

all: vet build

build:
	$(GO) build -trimpath -ldflags="-s -w" -o $(BIN)/tab-router ./cmd/tab-router

# Product targets: Windows and macOS. Linux is a dev/CI host only.
build-all:
	GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags="-s -w" -o $(BIN)/windows-amd64/tab-router.exe ./cmd/tab-router
	GOOS=darwin  GOARCH=arm64 $(GO) build -trimpath -ldflags="-s -w" -o $(BIN)/darwin-arm64/tab-router ./cmd/tab-router
	GOOS=darwin  GOARCH=amd64 $(GO) build -trimpath -ldflags="-s -w" -o $(BIN)/darwin-amd64/tab-router ./cmd/tab-router

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
	rm -rf $(BIN)
