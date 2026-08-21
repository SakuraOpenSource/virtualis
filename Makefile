BINARY      := virtualis
BIN_DIR     := bin
DIST_DIR    := internal/web/dist
FRONTEND    ?= ../virtualis-frontend
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X main.version=$(VERSION)
GO_ENV      := CGO_ENABLED=0

.PHONY: all build backend frontend vet fmt test clean

all: build

build: frontend backend

backend:
	@mkdir -p $(BIN_DIR)
	$(GO_ENV) go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) ./cmd/virtualis
	@echo "built $(BIN_DIR)/$(BINARY)"

frontend:
	@if [ ! -d "$(FRONTEND)" ]; then echo "frontend not found: $(FRONTEND)"; exit 1; fi
	pnpm --dir $(FRONTEND) install --frozen-lockfile
	pnpm --dir $(FRONTEND) build
	@rm -rf $(DIST_DIR)
	@mkdir -p $(DIST_DIR)
	@cp -R $(FRONTEND)/dist/. $(DIST_DIR)/
	@touch $(DIST_DIR)/.gitkeep
	@echo "frontend copied to $(DIST_DIR)"

vet:
	$(GO_ENV) go vet ./...

fmt:
	gofmt -w .

test:
	$(GO_ENV) go test ./...

clean:
	rm -rf $(BIN_DIR)
	rm -rf $(DIST_DIR)
	@mkdir -p $(DIST_DIR)
	@touch $(DIST_DIR)/.gitkeep
