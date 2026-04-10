.PHONY: lint fmt vet test govulncheck check

GOLANGCI_LINT ?= golangci-lint
GOFMT         ?= gofmt

lint:
	$(GOLANGCI_LINT) run ./...

fmt:
	$(GOFMT) -w .

vet:
	go vet ./...

test:
	go test ./...

govulncheck:
	go install golang.org/x/vuln/cmd/govulncheck@latest
	govulncheck ./...

check: vet lint test govulncheck
