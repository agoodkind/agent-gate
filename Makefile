GO_MK_URL   := https://raw.githubusercontent.com/agoodkind/go-makefile/main/go.mk
GO_MK       := .make/go.mk
GO_MK_CACHE := $(HOME)/.cache/go-makefile/go.mk

BINARY := agent-gate
CMD    := ./cmd/$(BINARY)
VPKG   := goodkind.io/agent-gate/internal/version
GKLOG_VPKG := goodkind.io/gklog/version

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

.PHONY: build deploy clean

build:
	go build -ldflags "$(LDFLAGS)" $(CMD)

deploy:
	go install -ldflags "$(LDFLAGS)" $(CMD)
	@echo "deployed: $$(go env GOPATH)/bin/$(BINARY)"

clean:
	rm -f $(BINARY)
