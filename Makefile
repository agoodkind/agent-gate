include vendor/go.mk/go.mk

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
