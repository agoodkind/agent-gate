GO_MK_URL := https://raw.githubusercontent.com/agoodkind/go-makefile/main/go.mk
GO_MK     := vendor/go.mk/go.mk

# Auto-download go.mk if missing. GNU Make re-reads after building an
# included file, so any target (make lint, make test, etc.) works on a
# fresh clone without a separate bootstrap step.
$(GO_MK):
	@mkdir -p $(dir $@)
	curl -fsSL $(GO_MK_URL) -o $@

-include $(GO_MK)

# Explicitly pull the latest go.mk (use in CI or to force an update locally).
.PHONY: sync
sync:
	@mkdir -p $(dir $(GO_MK))
	curl -fsSL $(GO_MK_URL) -o $(GO_MK)
	@echo "go.mk updated"

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
