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

BIN := bin/codegraph

.PHONY: build test golden bench clean

build:
	go build -o $(BIN) ./cmd/codegraph

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

clean:
	rm -rf bin
