# Barnraise development targets. Run `make help` for the list.

GO        ?= go
BIN       ?= barnraise
PKG       := ./cmd/barnraise
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo dev)
BUILD_TIME ?= $(shell git log -1 --format=%cI 2>/dev/null)
LDFLAGS   := -s -w -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)
COVER_MIN ?= 90
COVER_PKGS := ./internal/domain ./internal/app
FUZZTIME  ?= 10s
IMAGE     ?= barnraise:dev
BUF       ?= buf
CROSS_TARGETS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64
SMOKE_PROVIDER ?= fake
# Build tags. `tsnet` embeds Tailscale (about 13 MB more); releases carry it.
TAGS      ?=
RELEASE_TAGS ?= tsnet

.DEFAULT_GOAL := build
.PHONY: help brand web web-check desktop desktop-dev desktop-install desktop-uninstall build build-tsnet tsnet-check cross release-cross test test-race cover lint vuln proto proto-breaking e2e smoke fuzz-smoke docker clean

help: ## Show the targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z0-9-]+:.*## / {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

brand: ## Rebuild the logo, the panel's icons and the desktop app's icons from scripts/brand/build.py (needs Inkscape, Inter, Pillow)
	python3 scripts/brand/build.py

web: ## Build the panel (web/, Svelte) into the daemon's embedded files and the desktop launcher
	cd web && pnpm install --frozen-lockfile && pnpm gen && pnpm check && pnpm build && pnpm build:desktop

web-check: ## Fail when the committed panel build does not match web/
	$(MAKE) web
	git diff --exit-code -- internal/adapters/in/panel/dist web/src/lib/gen

desktop: web ## Build the desktop app (desktop/, Wails: needs cgo and WebKitGTK 4.1) with barnraise next to it
	$(MAKE) build BIN=desktop/build/bin/barnraise
	cd desktop && wails build -tags webkit2_41 -skipbindings -s -ldflags "-X main.appVersion=$(VERSION)"
	@# Wails makes the macOS icon by downscaling appicon.png; put the one drawn
	@# for each size in its place, and name the bundle as the app is named.
	@if [ "$$(uname)" = Darwin ] && [ -d desktop/build/bin/barnraise-desktop.app ]; then \
		cp desktop/build/darwin/iconfile.icns desktop/build/bin/barnraise-desktop.app/Contents/Resources/iconfile.icns; \
		rm -rf desktop/build/bin/Barnraise.app; \
		mv desktop/build/bin/barnraise-desktop.app desktop/build/bin/Barnraise.app; \
		touch desktop/build/bin/Barnraise.app; \
	fi

DESKTOP_ID := io.github.leandrodaf.barnraise
PREFIX     ?= $(HOME)/.local

desktop-install: ## Linux: install the built desktop app for this user (PREFIX=~/.local) with its launcher entry and icons
	@test "$$(uname)" = Linux || { echo "desktop-install is for Linux; on macOS drag Barnraise.app to Applications"; exit 1; }
	@test -x desktop/build/bin/barnraise-desktop || { echo "run make desktop first"; exit 1; }
	install -Dm755 desktop/build/bin/barnraise-desktop $(PREFIX)/bin/barnraise-desktop
	sed 's|^Exec=.*|Exec=$(PREFIX)/bin/barnraise-desktop|; s|^TryExec=.*|TryExec=$(PREFIX)/bin/barnraise-desktop|' \
		desktop/build/linux/$(DESKTOP_ID).desktop > desktop/build/bin/$(DESKTOP_ID).desktop
	install -Dm644 desktop/build/bin/$(DESKTOP_ID).desktop $(PREFIX)/share/applications/$(DESKTOP_ID).desktop
	install -Dm644 desktop/build/linux/$(DESKTOP_ID).metainfo.xml $(PREFIX)/share/metainfo/$(DESKTOP_ID).metainfo.xml
	cd desktop/build/linux/icons && find hicolor -type f -exec install -Dm644 {} $(PREFIX)/share/icons/{} \;
	-gtk-update-icon-cache -q -t -f $(PREFIX)/share/icons/hicolor 2>/dev/null
	-update-desktop-database -q $(PREFIX)/share/applications 2>/dev/null
	@echo "installed: $(PREFIX)/bin/barnraise-desktop (barnraise itself is found on PATH)"

desktop-uninstall: ## Linux: remove what desktop-install put in PREFIX
	rm -f $(PREFIX)/bin/barnraise-desktop $(PREFIX)/share/applications/$(DESKTOP_ID).desktop $(PREFIX)/share/metainfo/$(DESKTOP_ID).metainfo.xml
	find $(PREFIX)/share/icons/hicolor -name '$(DESKTOP_ID).*' -delete 2>/dev/null || true
	-gtk-update-icon-cache -q -t -f $(PREFIX)/share/icons/hicolor 2>/dev/null
	-update-desktop-database -q $(PREFIX)/share/applications 2>/dev/null

desktop-dev: ## The desktop app with live reload, on the panel's dev server (pnpm dev --port 5180 in web/)
	cd desktop && BARNRAISE_PANEL_DEV=http://localhost:5180 WAILS_DEV_ALLOWED_HOSTS=wails.localhost wails dev -tags webkit2_41 -skipbindings

build: ## Build ./barnraise with the version from git describe
	CGO_ENABLED=0 $(GO) build -trimpath -tags "$(TAGS)" -ldflags "$(LDFLAGS)" -o $(BIN) $(PKG)

build-tsnet: ## Build ./barnraise with the embedded Tailscale, as the releases are
	$(MAKE) build TAGS=$(RELEASE_TAGS)

tsnet-check: ## Build, vet and test the embedded Tailscale (tsnet tag; local test tailnet, no internet)
	$(GO) build -tags tsnet ./...
	$(GO) vet -tags tsnet ./...
	$(GO) test -tags tsnet -race -run 'TestEmbedded|TestResolve|TestOpen' ./internal/adapters/out/tailnet

test: ## Fast tests (-short: skips the end-to-end and cluster tests)
	$(GO) test -short ./...

test-race: ## Full suite with the race detector, end-to-end and cluster tests included
	$(GO) test -race -timeout 20m ./...

cover: ## Coverage profile; fails when internal/domain or internal/app is below COVER_MIN (90%)
	$(GO) test -race -short -covermode=atomic -coverprofile=coverage.out ./...
	@# One run feeds both: each package's own lines are cut out of the profile.
	@fail=0; mod=$$($(GO) list -m); for p in $(COVER_PKGS); do \
	  n=$$(basename $$p); pkg=$$mod/$${p#./}; \
	  { head -1 coverage.out; grep "^$$pkg/[^/]*:" coverage.out; } > coverage.$$n.out; \
	  pct=$$($(GO) tool cover -func=coverage.$$n.out | awk '/^total:/ {sub("%","",$$3); print $$3}'); \
	  ok=$$(awk -v p="$$pct" -v m="$(COVER_MIN)" 'BEGIN {print (p+0 >= m+0) ? 1 : 0}'); \
	  if [ "$$ok" = 1 ]; then echo "coverage $$p: $$pct% (minimum $(COVER_MIN)%)"; \
	  else echo "coverage $$p: $$pct% is below the minimum of $(COVER_MIN)%"; fail=1; fi; \
	done; exit $$fail

cross: ## Build linux and darwin, amd64 and arm64, into dist/ (in parallel)
	@mkdir -p dist; pids=""; for t in $(CROSS_TARGETS); do \
	  os=$${t%/*}; arch=$${t#*/}; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -tags "$(TAGS)" -ldflags "$(LDFLAGS)" -o dist/$(BIN)-$$os-$$arch $(PKG) & pids="$$pids $$!"; \
	done; fail=0; for pid in $$pids; do wait $$pid || fail=1; done; ls -l dist; exit $$fail

release-cross: ## The four release binaries into dist/, with the release build tags (tsnet)
	$(MAKE) cross TAGS=$(RELEASE_TAGS)

lint: ## golangci-lint, including the depguard architecture rules
	golangci-lint run ./...

vuln: ## govulncheck of barnraise and of the desktop app (its own module)
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...
	@test -e desktop/frontend/dist/index.html || { mkdir -p desktop/frontend/dist && echo '<!doctype html>' > desktop/frontend/dist/index.html; }
	cd desktop && $(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

proto: ## buf lint and buf generate (Go, Connect, OpenAPI into gen/)
	$(BUF) lint
	$(BUF) generate

proto-breaking: ## buf breaking against main
	$(BUF) breaking --against '.git#branch=main'

e2e: ## End-to-end and cluster tests only (need git)
	$(GO) test -race -timeout 20m -run 'TestEndToEnd|TestTwoMachines' -v ./cmd/barnraise

smoke: build ## init to one integrated item on a scratch repository (SMOKE_PROVIDER=fake|claude; claude spends tokens)
	scripts/smoke.sh $(SMOKE_PROVIDER)

fuzz-smoke: ## Run every fuzz target for FUZZTIME (default 10s)
	@set -e; for pkg in $$($(GO) list ./...); do \
	  for t in $$($(GO) test -list '^Fuzz' $$pkg 2>/dev/null | grep '^Fuzz' || true); do \
	    echo "fuzz $$pkg $$t ($(FUZZTIME))"; \
	    $(GO) test -run '^$$' -fuzz "^$$t$$" -fuzztime $(FUZZTIME) $$pkg; \
	  done; \
	done

docker: ## Build the container image (IMAGE, default barnraise:dev)
	docker build --build-arg VERSION=$(VERSION) --build-arg TAGS="$(TAGS)" -t $(IMAGE) .

clean: ## Remove build and coverage outputs
	rm -f $(BIN) coverage*.out
	rm -rf dist
