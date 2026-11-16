# bleak-node Makefile
#
# Common targets:
#   make         - build the server binary into bin/
#   make run     - build (if needed) and run the server on 0.0.0.0:8080
#   make test    - run go tests
#   make swagger - regenerate swagger docs (requires `go install github.com/swaggo/swag/cmd/swag@latest`)
#   make clean   - remove build artifacts

GO      ?= go
BIN     := bin/bleak-node
BINCTL  := bin/tokenctl
ADDR    ?= 0.0.0.0:8080
DB      ?= bleak-node.db

.PHONY: all run test swagger tokenctl migrate-legacy clean

all: $(BIN) $(BINCTL)

$(BIN): $(shell find cmd internal -name '*.go' 2>/dev/null) go.mod
	$(GO) build -o $@ ./cmd/server

$(BINCTL): $(shell find cmd internal -name '*.go' 2>/dev/null) go.mod
	$(GO) build -o $@ ./cmd/tokenctl

# Run the server. Rebuilds when sources are newer than the binary.
run: $(BIN)
	BLEAK_NODE_ADDR=$(ADDR) BLEAK_NODE_DB=$(DB) ./$(BIN)

# Mint a one-time room-creation token (raw printed once, hash stored).
tokenctl: $(BINCTL)

# Legacy migration: random tokens for every room & persona, an initial
# live mutual code per persona, one invite per room. Raw secrets are
# appended to the gitignored data/legacy-tokens.txt.
migrate-legacy: $(BINCTL)
	BLEAK_NODE_DB=$(DB) ./$(BINCTL) migrate

test:
	$(GO) test ./...

swagger:
	swag init --generalInfo cmd/server/main.go --output docs --parseInternal

clean:
	rm -rf bin/