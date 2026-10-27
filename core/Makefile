GO ?= go
BIN := bin
DIST := dist
VERSION := 0.1.0
LDFLAGS := -s -w

.PHONY: all build test vet e2e dist run-core run-boxd clean

all: vet test build

build:
	$(GO) build -o $(BIN)/ ./cmd/...

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

e2e: build
	SKIP_BUILD=1 ./scripts/e2e.sh

# Static binaries for hosts (cloud-init downloads boxd-linux-<arch> from core /dl/).
dist:
	@mkdir -p $(DIST)
	for arch in amd64 arm64; do \
	  for cmd in boxaz-core boxd box; do \
	    CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/$$cmd-linux-$$arch ./cmd/$$cmd || exit 1; \
	  done; \
	done
	@ls -la $(DIST)

# Local dev: core on :8080 with a fixed admin key + local boxd (no isolation).
run-core: build
	BOXAZ_DATA_DIR=./data BOXAZ_ADMIN_KEY=bx_admin_dev BOXAZ_BOOTSTRAP_TOKEN=dev-boot $(BIN)/boxaz-core

run-boxd: build
	BOXD_CORE_URL=http://127.0.0.1:8080 BOXD_TOKEN=dev-boot BOXD_DRIVER=local BOXD_STATE_DIR=./boxd-state $(BIN)/boxd

clean:
	rm -rf $(BIN) $(DIST)
