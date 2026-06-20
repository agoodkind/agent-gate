# Lint is centralized in go-makefile. Do NOT define project-local lint,
# deadcode, audit, fmt, vet, or staticcheck targets here. They duplicate
# the central pipeline and let agents bypass strict rules. Run `make help`
# for the canonical entry points (build/check/lint/fmt) and per-linter
# sub-targets (lint-golangci, lint-format, lint-gocyclo, lint-deadcode,
# staticcheck-extra). Refresh baselines via the matching *-baseline target.
#
# agent-gate Makefile.
# Build/lint/release pipeline lives in go-makefile and is fetched at runtime.
# Local daemon deployment uses the shared build/sign/install path, then restarts
# the service through go-service.mk.

# Optional local overrides (signing identity, never committed). Copy config.mk.example.
-include config.mk

# Identity
BINARY     := agent-gate
CMD        := ./cmd/$(BINARY)
VPKG       := goodkind.io/agent-gate/internal/version
GKLOG_VPKG := goodkind.io/gklog/version

# CGO=1 for the daemon's runtime requirements.
export CGO_ENABLED := 1

# Build tags. sqlite_fts5 makes mattn/go-sqlite3 compile the FTS5 extension,
# which the durable intake store's command-search index depends on. Driving the
# tag through GOFLAGS (rather than GO_BUILD_TAGS) makes every `go` subcommand
# pick it up uniformly: the build path, the go-mk test helper, and vet all link
# FTS5. Exported so recipe subprocesses inherit it.
export GOFLAGS := -tags=sqlite_fts5

# Daemon identity. go-service.mk reads these at parse time, so they must be
# set BEFORE -include $(GO_MK).
LAUNCHD_LABEL := io.goodkind.agent-gate
SYSTEMD_UNIT  := agent-gate.service
LOG_PATH      := $(or $(XDG_STATE_HOME),$(HOME)/.local/state)/agent-gate/agent-gate.log
BUNDLE_ID             ?= io.goodkind.agent-gate
CODESIGN_ENTITLEMENTS := packaging/macos/agent-gate.entitlements

# go-mk's release command signs darwin binaries with quill and reads the
# hardened-runtime entitlements (which let the Homebrew PCRE2 dylib load) from
# RELEASE_ENTITLEMENTS.
RELEASE_ENTITLEMENTS  := $(CODESIGN_ENTITLEMENTS)

# Pipeline modules
GO_MK_MODULES := go-build.mk go-release.mk go-service.mk

# go.mk runs these as order-only prerequisites of every build, lint, vet, test,
# and govulncheck target (and install/release via the modules). GO_MK_GENERATE
# generates the tree-sitter parsers; GO_MK_WORKSPACE_USE materializes a
# gitignored go.work that routes the gksyntax submodule into the build, so both
# exist before any target compiles the grammar packages.
GO_MK_GENERATE := gksyntax-grammars
GO_MK_WORKSPACE_USE := . third_party/gksyntax

include bootstrap.mk

.DEFAULT_GOAL := check

# ---------------------------------------------------------------------------
# gksyntax submodule grammars
# ---------------------------------------------------------------------------
# The exec gate's shell decomposition lives in goodkind.io/gksyntax, a git
# submodule under third_party/ routed through a generated, gitignored go.work
# (GO_MK_WORKSPACE_USE above). A plain module require is not possible because
# gksyntax vendors the dart and swift grammars as its own submodules, whose C
# sources are absent from a Go module zip, and a go.mod replace is rejected by
# gomoddirectives. gksyntax commits only the swift grammar definition, not the
# generated parser, so the parser is produced from the pinned submodule by the
# tree-sitter CLI. The generated parser stays inside the submodule working tree
# (gitignored there) and is never committed.
GKS_DIR := third_party/gksyntax
SWIFT_GRAMMAR_DIR := $(GKS_DIR)/treesitter/grammars/swift/upstream
SWIFT_GRAMMAR_DEF := $(SWIFT_GRAMMAR_DIR)/src/grammar.json
SWIFT_GRAMMAR_PARSER := $(SWIFT_GRAMMAR_DIR)/src/parser.c
PERL_GRAMMAR_DIR := $(GKS_DIR)/treesitter/grammars/perl/upstream
PERL_GRAMMAR_DEF := $(PERL_GRAMMAR_DIR)/src/grammar.json
PERL_GRAMMAR_PARSER := $(PERL_GRAMMAR_DIR)/src/parser.c
TREE_SITTER_ABI ?= 14
# tree-sitter CLI lands here when the host has none on PATH. Gitignored.
TREE_SITTER_LOCAL_DIR := $(CURDIR)/.bin

# gksyntax commits the swift and perl grammar definitions (and their external
# scanners) but not the generated parsers, so each parser is produced from the
# pinned submodule by the tree-sitter CLI. Swift commits its own parser.c in
# upstream and is restored with `git checkout -- .` after generation; perl
# commits no parser.c, so its generated parser.c and tree_sitter/ headers are
# kept in place and the perl submodule tree is not reset.
.PHONY: gksyntax-grammars
gksyntax-grammars:
	@git submodule update --init $(GKS_DIR)
	@git -C $(GKS_DIR) submodule update --init \
		treesitter/grammars/swift/upstream \
		treesitter/grammars/perl/upstream \
		treesitter/grammars/awk/upstream \
		treesitter/grammars/dart/upstream
	@if [ ! -f "$(SWIFT_GRAMMAR_DEF)" ]; then \
		echo "gksyntax-grammars: $(SWIFT_GRAMMAR_DIR) is empty; run 'git submodule update --init --recursive'"; \
		exit 1; \
	fi
	@if [ ! -f "$(PERL_GRAMMAR_DEF)" ]; then \
		echo "gksyntax-grammars: $(PERL_GRAMMAR_DIR) is empty; run 'git submodule update --init --recursive'"; \
		exit 1; \
	fi
	@ts_bin="$$(command -v tree-sitter 2>/dev/null || true)"; \
	if [ -z "$$ts_bin" ]; then \
		"$(GKS_DIR)/scripts/install-tree-sitter.sh" "$(TREE_SITTER_LOCAL_DIR)"; \
		ts_bin="$(TREE_SITTER_LOCAL_DIR)/tree-sitter"; \
	fi; \
	if [ ! -f "$(SWIFT_GRAMMAR_PARSER)" ] || [ "$(SWIFT_GRAMMAR_DEF)" -nt "$(SWIFT_GRAMMAR_PARSER)" ]; then \
		echo "gksyntax-grammars: generating Swift parser (abi $(TREE_SITTER_ABI))"; \
		( cd "$(SWIFT_GRAMMAR_DIR)" && "$$ts_bin" generate src/grammar.json --abi $(TREE_SITTER_ABI) ); \
		git -C "$(SWIFT_GRAMMAR_DIR)" checkout -- . >/dev/null 2>&1 || true; \
	else \
		echo "gksyntax-grammars: Swift parser already generated"; \
	fi; \
	if [ ! -f "$(PERL_GRAMMAR_PARSER)" ] || [ "$(PERL_GRAMMAR_DEF)" -nt "$(PERL_GRAMMAR_PARSER)" ]; then \
		echo "gksyntax-grammars: generating Perl parser (abi $(TREE_SITTER_ABI))"; \
		( cd "$(PERL_GRAMMAR_DIR)" && "$$ts_bin" generate src/grammar.json --abi $(TREE_SITTER_ABI) ); \
	else \
		echo "gksyntax-grammars: Perl parser already generated"; \
	fi

# The order-only prerequisite that runs gksyntax-grammars before every compile,
# vet, lint, test, install, and release target is wired centrally in go.mk via
# GO_MK_GENERATE (set above), so no per-target list is maintained here.

# ---------------------------------------------------------------------------
# Project-local
# ---------------------------------------------------------------------------

# Daemon control comes from go-service.mk: service-install, service-uninstall,
# service-restart, service-status. Templates live at packaging/{macos,systemd}/.

.PHONY: proto smoke-build deploy deploy-service install-release install-release-bin install-release-hooks install-release-service \
        daemon-wait daemon-status spawn-smoke

proto:
	buf generate

# smoke-build produces a signed binary at $(OUT) (default .make/smoke/agent-gate)
# through the shared go-build.mk build/sign path.
smoke-build:
	@out="$${OUT:-.make/smoke/agent-gate}"; \
	dist_dir="$$(dirname "$$out")"; \
	$(MAKE) BUILD_CHECKS=false DIST_DIR="$$dist_dir" build; \
	if [ "$$out" != "$$dist_dir/$(BINARY)" ]; then \
		cp -f "$$dist_dir/$(BINARY)" "$$out"; \
	fi; \
	echo "smoke-built: $$out"

deploy:
	$(MAKE) BUILD_CHECKS=false install
	$(MAKE) deploy-service
	$(MAKE) daemon-wait
	$(MAKE) daemon-status

# deploy-service restarts the supervised daemon. On macOS it fully unloads the
# service (bootout), waits for the process to exit, then loads it again
# (bootstrap), rather than `launchctl kickstart -k`. kickstart can start the new
# instance before the old one releases the SQLite WAL lock, which makes the
# daemon crash-loop on "database is locked" during the startup intake replay.
# bootout + wait + bootstrap guarantees no instance overlap. Linux keeps
# service-restart because systemctl restart is already overlap-free.
deploy-service:
	@if [ "$$(uname)" = "Darwin" ]; then \
		echo "restarting $(LAUNCHD_LABEL): bootout + bootstrap"; \
		launchctl bootout "$(LAUNCHD_DOMAIN)/$(LAUNCHD_LABEL)" 2>/dev/null || true; \
		for _ in $$(seq 1 50); do \
			launchctl print "$(LAUNCHD_DOMAIN)/$(LAUNCHD_LABEL)" >/dev/null 2>&1 || break; \
			sleep 0.2; \
		done; \
		if ! launchctl bootstrap "$(LAUNCHD_DOMAIN)" "$(LAUNCHD_PLIST)" 2>/dev/null; then \
			echo "bootstrap failed; (re)installing user service"; \
			$(MAKE) service-install; \
			launchctl enable "$(LAUNCHD_DOMAIN)/$(LAUNCHD_LABEL)" 2>/dev/null || true; \
			launchctl bootstrap "$(LAUNCHD_DOMAIN)" "$(LAUNCHD_PLIST)" 2>/dev/null || true; \
		fi; \
	else \
		$(MAKE) service-restart || { \
			echo "service restart failed; installing user service"; \
			$(MAKE) service-install; \
			$(MAKE) service-restart; \
		}; \
	fi

# install-release fetches the latest release via install.sh. Distinct from
# canonical `make install` which atomically copies the locally-built binary
# into $XDG_BIN_HOME.
install-release:
	./install.sh $(ARGS)

install-release-bin:
	./install.sh --bin-only $(ARGS)

install-release-hooks:
	./install.sh --hooks-only $(ARGS)

install-release-service:
	./install.sh --service-only --bin-dir $(INSTALL_DIR) $(ARGS)

# daemon-status calls the agent-gate CLI's own status subcommand, which is
# richer than launchctl/systemctl status. service-status from go-service.mk
# is also available for the platform-level view.
daemon-status:
	$(INSTALL_BIN) daemon status

daemon-wait:
	@for attempt in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do \
		if "$(INSTALL_BIN)" daemon status >/dev/null 2>&1; then \
			exit 0; \
		fi; \
		sleep 0.25; \
	done; \
	"$(INSTALL_BIN)" daemon status

spawn-smoke:
	go run ./cmd/spawn-smoke -input-file "$(INPUT)" $(ARGS)
