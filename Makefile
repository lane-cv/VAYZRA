GO ?= go
PNPM ?= pnpm
GOBIN ?= $(CURDIR)/.tools/bin
GOVULNCHECK := $(GOBIN)/govulncheck

.PHONY: test-go test-web tools verify e2e

test-go:
	$(GO) test ./...

test-web:
	$(PNPM) test
	$(PNPM) typecheck
	$(PNPM) build

tools: $(GOVULNCHECK)

$(GOVULNCHECK): go.mod go.sum
	@mkdir -p $(GOBIN)
	GOBIN=$(GOBIN) $(GO) install golang.org/x/vuln/cmd/govulncheck@v1.1.4

verify: tools
	$(GO) test -race ./...
	$(GO) vet ./...
	$(GOVULNCHECK) ./...
	$(PNPM) test
	$(PNPM) typecheck
	$(PNPM) build
	docker compose -f deploy/compose.dev.yml config --quiet

e2e:
	bash scripts/e2e-disposable.sh
