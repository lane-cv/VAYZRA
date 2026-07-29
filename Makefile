GO ?= go
PNPM ?= pnpm
GOBIN ?= $(CURDIR)/.tools/bin
GOVULNCHECK := $(GOBIN)/govulncheck

.PHONY: test-go test-web tools verify e2e e2e-phase2 e2e-phase3 e2e-phase4 e2e-contracts phase5-backup-contract phase5-backup-live phase5-backup phase5-restore-contract

BACKUP_TRIGGER ?= manual

test-go:
	$(GO) test ./...

test-web:
	$(PNPM) test
	$(PNPM) typecheck
	$(PNPM) lint
	$(PNPM) build

tools: $(GOVULNCHECK)

$(GOVULNCHECK): go.mod go.sum
	@mkdir -p $(GOBIN)
	GOBIN=$(GOBIN) $(GO) install golang.org/x/vuln/cmd/govulncheck@v1.6.0

verify: tools e2e-contracts
	$(GO) test -race ./...
	$(GO) vet ./...
	$(GOVULNCHECK) ./...
	$(PNPM) test
	$(PNPM) typecheck
	$(PNPM) lint
	$(PNPM) build
	docker compose -f deploy/compose.dev.yml config --quiet

e2e:
	bash scripts/e2e-phase2.sh

e2e-phase2:
	bash scripts/e2e-phase2.sh

e2e-phase3:
	bash scripts/e2e-phase3.sh

e2e-phase4:
	bash scripts/e2e-phase4.sh

e2e-contracts:
	bash scripts/ci-compose_contract_test.sh
	bash scripts/ci-compose_contract_mutation_test.sh
	bash scripts/ci-goenv_contract_test.sh
	bash scripts/copy-e2e-workspace_test.sh
	bash scripts/e2e-phase2_contract_test.sh
	bash scripts/e2e-phase3_contract_test.sh
	bash scripts/e2e-phase4_contract_test.sh
	bash scripts/e2e-harness_semantics_contract_test.sh
	bash scripts/e2e-artifact-sanitization_contract_test.sh

phase5-backup-contract:
	bash scripts/phase5-backup_contract_test.sh

phase5-backup-live:
	bash scripts/phase5-backup_live_test.sh

phase5-backup:
	bash scripts/phase5-backup.sh --project happylearn-dev --trigger $(BACKUP_TRIGGER)

phase5-restore-contract:
	bash scripts/phase5-restore_contract_test.sh
