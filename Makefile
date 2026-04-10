BINARY := hookguard
CMD    := ./cmd/$(BINARY)

.DEFAULT_GOAL := build

.PHONY: build deploy test clean

build:
	go build $(CMD)

deploy:
	go install $(CMD)
	@echo "deployed: $$(go env GOPATH)/bin/$(BINARY)"

test:
	go test -v -race ./...

clean:
	rm -f $(BINARY)
