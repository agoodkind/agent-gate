GO_MK_URL   := https://raw.githubusercontent.com/agoodkind/go-makefile/main/go.mk
GO_MK       := vendor/go.mk/go.mk
GO_MK_CACHE := $(HOME)/.cache/go-makefile/go.mk

# Auto-download go.mk if missing. On success, update the local cache.
# On failure, fall back to the last known good cache. If neither exists, fail.
# GNU Make re-reads after building an included file, so any target works
# on a fresh clone without a separate bootstrap step.
$(GO_MK):
	@mkdir -p $(dir $@)
	@if curl -fsSL --connect-timeout 5 --max-time 10 "$(GO_MK_URL)" -o "$@"; then \
		mkdir -p "$(dir $(GO_MK_CACHE))" && cp "$@" "$(GO_MK_CACHE)"; \
	elif [ -f "$(GO_MK_CACHE)" ]; then \
		echo "warning: go.mk fetch failed, using cached version" >&2; \
		cp "$(GO_MK_CACHE)" "$@"; \
	else \
		echo "error: go.mk fetch failed and no cache available" >&2; \
		exit 1; \
	fi

-include $(GO_MK)

# Explicitly pull the latest go.mk and update the cache.
.PHONY: sync
sync:
	@mkdir -p "$(dir $(GO_MK))"
	@if curl -fsSL --connect-timeout 5 --max-time 10 "$(GO_MK_URL)" -o "$(GO_MK)"; then \
		mkdir -p "$(dir $(GO_MK_CACHE))" && cp "$(GO_MK)" "$(GO_MK_CACHE)"; \
		echo "go.mk updated"; \
	else \
		echo "error: go.mk fetch failed" >&2; \
		exit 1; \
	fi

BINARY := agent-gate
CMD    := ./cmd/$(BINARY)

.DEFAULT_GOAL := build

.PHONY: build deploy clean

build:
	go build $(CMD)

deploy:
	go install $(CMD)
	@echo "deployed: $$(go env GOPATH)/bin/$(BINARY)"

clean:
	rm -f $(BINARY)
