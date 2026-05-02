GO_MK_URL   := https://raw.githubusercontent.com/agoodkind/go-makefile/main/go.mk
GO_MK       := .make/go.mk
GO_MK_CACHE := $(or $(XDG_CACHE_HOME),$(HOME)/.cache)/go-makefile/go.mk

# Optional local overrides (signing identity, never committed). Copy config.mk.example.
-include config.mk

BINARY := agent-gate
CMD    := ./cmd/$(BINARY)
VPKG   := goodkind.io/agent-gate/internal/version
GKLOG_VPKG := goodkind.io/gklog/version

DIST_DIR    := dist
DIST_BIN    := $(DIST_DIR)/$(BINARY)

# XDG_BIN_HOME is the spec-aligned per-user binary dir. The XDG spec
# defaults to ~/.local/bin when unset.
INSTALL_DIR := $(or $(XDG_BIN_HOME),$(HOME)/.local/bin)
INSTALL_BIN := $(INSTALL_DIR)/$(BINARY)
LAUNCHD_LABEL := io.goodkind.agent-gate
LAUNCHD_PLIST := $(HOME)/Library/LaunchAgents/$(LAUNCHD_LABEL).plist
LAUNCHD_DOMAIN := gui/$(shell id -u)
SYSTEMD_UNIT := agent-gate.service
BUNDLE_ID ?= io.goodkind.agent-gate
ENTITLEMENTS := packaging/macos/agent-gate.entitlements
CODESIGN_IDENTITY := $(or $(CERT_ID),$(shell if [ "$$(uname -s)" = "Darwin" ]; then security find-identity -v -p codesigning 2>/dev/null | awk '/Developer ID Application/ { print $$2; exit }'; fi))

GIT_COMMIT  := $(shell git rev-parse --short HEAD)
GIT_VERSION := $(shell git describe --tags --always --dirty)
GIT_DIRTY   := $(shell git diff --quiet && echo false || echo true)
BUILD_TIME  := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -X $(VPKG).Commit=$(GIT_COMMIT) \
           -X $(VPKG).Version=$(GIT_VERSION) \
           -X $(VPKG).Dirty=$(GIT_DIRTY) \
           -X $(GKLOG_VPKG).Commit=$(GIT_COMMIT) \
           -X $(GKLOG_VPKG).Dirty=$(GIT_DIRTY) \
           -X $(GKLOG_VPKG).BuildTime=$(BUILD_TIME) \
           -X $(GKLOG_VPKG).BinHash=

# Auto-download go.mk if missing. On success, update the local cache.
# On failure, fall back to the last known good cache. If neither exists, fail.
# GNU Make re-reads after building an included file, so any target works
# on a fresh clone without a separate bootstrap step.
# BINARY and CMD are defined above so go.mk's 'ifndef CMD' sees us as a
# binary project and skips its default library-style 'build' recipe.
$(GO_MK):
	@[ -f "$@" ] && exit 0; \
	mkdir -p $(dir $@); \
	if curl -fsSL --connect-timeout 5 --max-time 10 "$(GO_MK_URL)" -o "$@"; then \
		mkdir -p "$(dir $(GO_MK_CACHE))" && cp "$@" "$(GO_MK_CACHE)"; \
	elif [ -f "$(GO_MK_CACHE)" ]; then \
		echo "warning: go.mk fetch failed, using cached version"; \
		cp "$(GO_MK_CACHE)" "$@"; \
	else \
		echo "error: go.mk fetch failed and no cache available"; \
		exit 1; \
	fi

-include $(GO_MK)

.DEFAULT_GOAL := check

.PHONY: proto build smoke-build install install-bin install-hooks install-service uninstall deploy deploy-bin daemon-start daemon-stop daemon-restart daemon-status clean spawn-smoke

proto:
	buf generate

# build compiles a local dev binary to dist/agent-gate. Used for iteration.
# `make install` does NOT use this output: it pulls the latest release.
build:
	@mkdir -p $(DIST_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(DIST_BIN) $(CMD)
	@if [ "$$(uname -s)" = "Darwin" ]; then \
		if [ -z "$(CODESIGN_IDENTITY)" ]; then \
			echo "No Developer ID Application signing identity found."; \
			echo "Set CERT_ID in config.mk or install a Developer ID Application certificate."; \
			exit 1; \
		fi; \
		echo "Signing $(DIST_BIN) with $(CODESIGN_IDENTITY)..."; \
		codesign --force --sign "$(CODESIGN_IDENTITY)" --identifier "$(BUNDLE_ID)" --options runtime --timestamp=none --entitlements "$(ENTITLEMENTS)" "$(DIST_BIN)"; \
		codesign --verify --verbose=2 "$(DIST_BIN)"; \
	fi
	@echo "built: $(DIST_BIN)"

smoke-build:
	@out="$${OUT:-.make/smoke/agent-gate}"; \
	version="$${VERSION:-smoke}"; \
	commit="$${COMMIT:-smoke}"; \
	mkdir -p "$$(dirname "$$out")"; \
	go build -ldflags "-X $(VPKG).Commit=$$commit -X $(VPKG).Version=$$version -X $(VPKG).Dirty=true -X $(GKLOG_VPKG).Commit=$$commit -X $(GKLOG_VPKG).Dirty=true -X $(GKLOG_VPKG).BuildTime=$(BUILD_TIME) -X $(GKLOG_VPKG).BinHash=" -o "$$out" $(CMD); \
	if [ "$$(uname -s)" = "Darwin" ]; then \
		if [ -z "$(CODESIGN_IDENTITY)" ]; then \
			echo "No Developer ID Application signing identity found."; \
			exit 1; \
		fi; \
		codesign --force --sign "$(CODESIGN_IDENTITY)" --identifier "$(BUNDLE_ID)" --options runtime --timestamp=none --entitlements "$(ENTITLEMENTS)" "$$out"; \
		codesign --verify --verbose=2 "$$out"; \
	fi; \
	echo "smoke-built: $$out"

# install runs install.sh which downloads the latest release for the host
# platform and wires hooks into Claude, Codex, and Gemini configs. Override
# behavior with flags, e.g. `make install ARGS=--bin-only`.
install:
	./install.sh $(ARGS)

install-bin:
	./install.sh --bin-only $(ARGS)

install-hooks:
	./install.sh --hooks-only $(ARGS)

install-service:
	./install.sh --service-only --bin-dir $(INSTALL_DIR) $(ARGS)

uninstall:
	@rm -f $(INSTALL_BIN)
	@echo "removed: $(INSTALL_BIN)"

# deploy builds the current working tree directly to the active per-user
# install path and restarts the user service under its native supervisor.
# This is intended for local iteration, unlike `make install` which downloads
# the latest release.
deploy: deploy-bin daemon-restart

deploy-bin:
	@mkdir -p $(INSTALL_DIR)
	@tmp="$$(mktemp -t agent-gate-install.XXXXXX)"; \
	out="$(INSTALL_BIN).new.$$$$"; \
	trap 'rm -f "$$tmp" "$$out"' EXIT; \
	set -e; \
	go build -ldflags "$(LDFLAGS)" -o "$$tmp" $(CMD); \
	test -s "$$tmp"; \
	chmod 0755 "$$tmp"; \
	if [ "$$(uname -s)" = "Darwin" ]; then \
		if [ -z "$(CODESIGN_IDENTITY)" ]; then \
			echo "No Developer ID Application signing identity found."; \
			echo "Set CERT_ID in config.mk or install a Developer ID Application certificate."; \
			exit 1; \
		fi; \
		echo "Signing install build with $(CODESIGN_IDENTITY)..."; \
		codesign --force --sign "$(CODESIGN_IDENTITY)" --identifier "$(BUNDLE_ID)" --options runtime --timestamp=none --entitlements "$(ENTITLEMENTS)" "$$tmp"; \
		codesign --verify --verbose=2 "$$tmp"; \
	fi; \
	cp -f "$$tmp" "$$out"; \
	chmod 0755 "$$out"; \
	test -s "$$out"; \
	mv -f "$$out" "$(INSTALL_BIN)"
	@echo "deployed: $(INSTALL_BIN)"

daemon-start: install-service

daemon-stop:
	@if [ "$$(uname -s)" = "Darwin" ]; then \
		launchctl bootout $(LAUNCHD_DOMAIN) "$(LAUNCHD_PLIST)" >/dev/null 2>&1 || true; \
		echo "daemon stopped: $(LAUNCHD_LABEL)"; \
	elif [ "$$(uname -s)" = "Linux" ]; then \
		systemctl --user stop $(SYSTEMD_UNIT); \
	else \
		echo "unsupported OS: $$(uname -s)"; exit 1; \
	fi

daemon-restart:
	@if [ "$$(uname -s)" = "Darwin" ]; then \
		./install.sh --service-only --bin-dir $(INSTALL_DIR) $(ARGS); \
	elif [ "$$(uname -s)" = "Linux" ]; then \
		./install.sh --service-only --bin-dir $(INSTALL_DIR) $(ARGS); \
	else \
		echo "unsupported OS: $$(uname -s)"; exit 1; \
	fi

daemon-status:
	$(INSTALL_BIN) daemon status

clean:
	rm -rf $(DIST_DIR)
	rm -f $(BINARY)

spawn-smoke:
	go run ./cmd/spawn-smoke -input-file "$(INPUT)" $(ARGS)
