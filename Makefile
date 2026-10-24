# Developer shortcuts. On Apple Silicon with an x86_64 (Rosetta) Go install,
# cgo (tree-sitter) can't use the arm64-only Command Line Tools, so we build
# native arm64 binaries with an explicit clang + SDK. With an arm64 Go this
# block does nothing.
GO_ARCH    := $(shell go env GOARCH)
APPLE_ARM  := $(shell sysctl -n hw.optional.arm64 2>/dev/null)
ifeq ($(GO_ARCH)-$(APPLE_ARM),amd64-1)
export GOARCH      := arm64
export CGO_ENABLED := 1
export CC          := /Library/Developer/CommandLineTools/usr/bin/clang -isysroot /Library/Developer/CommandLineTools/SDKs/MacOSX.sdk
endif

BIN := bin/uxor

.PHONY: build test golden bench eval install clean

build:
	go build -o $(BIN) ./cmd/uxor

test:
	go vet ./...
	go test -count=1 ./...

# Regenerate parser, resolver and query golden files and examples/ after an intended change;
# review the diff before committing.
golden:
	go test -count=1 ./internal/parse -run TestGolden -update
	go test -count=1 ./internal/resolve -run TestEdgesGolden -update
	go test -count=1 ./internal/retrieve -run TestQueryGolden -update
	go test -count=1 ./internal/setup -run TestExamples -update

# make bench REPO=/path/to/repo
bench: build
	@test -n "$(REPO)" || (echo "usage: make bench REPO=/path/to/repo" && exit 2)
	/usr/bin/time -l $(BIN) --root $(REPO) index --full
	/usr/bin/time -l $(BIN) --root $(REPO) index
	$(BIN) --root $(REPO) stats

# make install [PREFIX=~]: copy the binary to $(PREFIX)/bin. install(1)
# replaces the file instead of overwriting it in place; on macOS an in-place
# copy keeps a stale code signature and the new binary is killed on start.
PREFIX ?= $(HOME)
install: build
	mkdir -p $(PREFIX)/bin
	install -m 0755 $(BIN) $(PREFIX)/bin/uxor

# make eval REPO=/path/to/repo [N=100]: honest retrieval eval with token savings.
# The repo needs history: clone without --depth, or git fetch --deepen=400.
N ?= 100
eval: build
	@test -n "$(REPO)" || (echo "usage: make eval REPO=/path/to/repo [N=100]" && exit 2)
	$(BIN) --root $(REPO) eval --commits $(N) --checkout-parent --savings

clean:
	rm -rf bin
