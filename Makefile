# Barnraise development targets. Run `make help` for the list.

GO        ?= go
BIN       ?= barnraise
PKG       := ./cmd/barnraise
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)
COVER_MIN ?= 90
COVER_PKGS := ./internal/domain ./internal/app
FUZZTIME  ?= 10s
IMAGE     ?= barnraise:dev
BUF       ?= buf

.DEFAULT_GOAL := build
.PHONY: help build test test-race cover lint vuln proto proto-breaking e2e fuzz-smoke docker clean

help: ## Show the targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z0-9-]+:.*## / {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build ./barnraise with the version from git describe
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) $(PKG)

test: ## Fast tests (-short: skips the end-to-end and cluster tests)
	$(GO) test -short ./...

test-race: ## Full suite with the race detector, end-to-end and cluster tests included
	$(GO) test -race -timeout 20m ./...

cover: ## Coverage profile; fails when internal/domain or internal/app is below COVER_MIN (90%)
	$(GO) test -race -short -covermode=atomic -coverprofile=coverage.out ./...
	@fail=0; for p in $(COVER_PKGS); do \
	  n=$$(basename $$p); \
	  $(GO) test -short -covermode=atomic -coverprofile=coverage.$$n.out $$p >/dev/null || exit 1; \
	  pct=$$($(GO) tool cover -func=coverage.$$n.out | awk '/^total:/ {sub("%","",$$3); print $$3}'); \
	  ok=$$(awk -v p="$$pct" -v m="$(COVER_MIN)" 'BEGIN {print (p+0 >= m+0) ? 1 : 0}'); \
	  if [ "$$ok" = 1 ]; then echo "coverage $$p: $$pct% (minimum $(COVER_MIN)%)"; \
	  else echo "coverage $$p: $$pct% is below the minimum of $(COVER_MIN)%"; fail=1; fi; \
	done; exit $$fail

lint: ## golangci-lint, including the depguard architecture rules
	golangci-lint run ./...

vuln: ## govulncheck
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

proto: ## buf lint and buf generate (Go, Connect, OpenAPI into gen/)
	$(BUF) lint
	$(BUF) generate

proto-breaking: ## buf breaking against main
	$(BUF) breaking --against '.git#branch=main'

e2e: ## End-to-end and cluster tests only (need git)
	$(GO) test -race -timeout 20m -run 'TestEndToEnd|TestTwoMachines' -v ./cmd/barnraise

fuzz-smoke: ## Run every fuzz target for FUZZTIME (default 10s)
	@set -e; for pkg in $$($(GO) list ./...); do \
	  for t in $$($(GO) test -list '^Fuzz' $$pkg 2>/dev/null | grep '^Fuzz' || true); do \
	    echo "fuzz $$pkg $$t ($(FUZZTIME))"; \
	    $(GO) test -run '^$$' -fuzz "^$$t$$" -fuzztime $(FUZZTIME) $$pkg; \
	  done; \
	done

docker: ## Build the container image (IMAGE, default barnraise:dev)
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

clean: ## Remove build and coverage outputs
	rm -f $(BIN) coverage*.out
	rm -rf dist
