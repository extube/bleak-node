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
ADDR    ?= 0.0.0.0:8080
DB      ?= bleak-node.db

.PHONY: all run test swagger clean

all: $(BIN)

$(BIN): $(shell find cmd internal -name '*.go' 2>/dev/null) go.mod
	$(GO) build -o $@ ./cmd/server

# Run the server. Rebuilds when sources are newer than the binary.
run: $(BIN)
	BLEAK_NODE_ADDR=$(ADDR) BLEAK_NODE_DB=$(DB) ./$(BIN)

test:
	$(GO) test ./...

swagger:
	swag init --generalInfo cmd/server/main.go --output docs --parseInternal

clean:
	rm -rf bin/
