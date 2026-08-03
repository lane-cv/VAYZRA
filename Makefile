GO ?= go
SHELLCHECK ?= shellcheck
PNPM ?= pnpm
GOBIN ?= $(CURDIR)/.tools/bin
GOVULNCHECK := $(GOBIN)/govulncheck

.PHONY: test-go test-web tools verify e2e e2e-phase2 e2e-phase3 e2e-phase4 e2e-phase5 e2e-phase6 e2e-contracts phase5-backup-contract phase5-backup-live phase5-backup phase5-restore-contract phase5-restore-live-contract phase5-failure-matrix-contract phase5-operations-docs-contract phase6-production-contracts phase6-caddy-runtime-contract phase6-release-go-contracts phase6-host-contracts phase6-shellcheck phase6-release-contracts phase6-contracts host-sampler host-metrics-contract host-metrics-uid-contract host-metrics-live host-metrics

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

e2e-phase5:
	bash scripts/e2e-phase5.sh

e2e-phase6:
	bash scripts/e2e-phase6.sh

e2e-contracts: phase6-release-contracts
	bash scripts/ci-compose_contract_test.sh
	bash scripts/ci-compose_contract_mutation_test.sh
	bash scripts/ci-goenv_contract_test.sh
	bash scripts/copy-e2e-workspace_test.sh
	bash scripts/e2e-phase2_contract_test.sh
	bash scripts/e2e-phase3_contract_test.sh
	bash scripts/e2e-phase4_contract_test.sh
	bash scripts/e2e-phase5_contract_test.sh
	bash scripts/e2e-harness_semantics_contract_test.sh
	bash scripts/e2e-artifact-sanitization_contract_test.sh
	bash scripts/phase5-backup_contract_test.sh
	bash scripts/phase5-restore_contract_test.sh
	bash scripts/phase5-restore_live_contract_test.sh
	bash scripts/e2e-phase5_failure_matrix_contract_test.sh
	bash scripts/host-metrics_contract_test.sh
	bash scripts/host-metrics_uid_contract_test.sh
	bash scripts/phase5-operations-docs_contract_test.sh

phase6-production-contracts:
	bash scripts/phase6-production_contract_test.sh
	bash scripts/phase6-production_contract_mutation_test.sh

phase6-caddy-runtime-contract:
	HAPPYLEARN_PHASE6_CONTRACT_SCOPE=caddy-runtime bash scripts/phase6-production_contract_test.sh

phase6-release-go-contracts:
	$(GO) test ./internal/release ./cmd/release-manifest ./cmd/migrate ./cmd/acceptance ./cmd/release-control

phase6-host-contracts:
	bash scripts/prod-common_contract_test.sh
	bash scripts/prod-preflight_contract_test.sh
	bash scripts/prod-backup_contract_test.sh
	bash scripts/prod-release_contract_test.sh
	bash scripts/prod-rollback_contract_test.sh
	bash scripts/phase5-restore_preserve_contract_test.sh
	bash scripts/prod-restore_contract_test.sh
	bash scripts/phase6-release_failure_matrix_contract_test.sh
	bash scripts/phase6-systemd_contract_test.sh

phase6-shellcheck:
	$(SHELLCHECK) -x scripts/prod-common.sh scripts/prod-preflight.sh scripts/prod-backup.sh scripts/prod-release.sh scripts/prod-rollback.sh scripts/prod-restore.sh scripts/render-systemd.sh scripts/systemd-maintenance.sh scripts/phase6-release_failure_matrix.sh scripts/phase6-release_failure_matrix_adapter.sh scripts/phase6-release_failure_matrix_contract_test.sh scripts/phase6-systemd_contract_test.sh scripts/e2e-phase6.sh scripts/e2e-phase6_security.sh scripts/e2e-phase6_resources.sh

phase6-release-contracts: phase6-release-go-contracts phase6-production-contracts phase6-host-contracts phase6-shellcheck

phase6-contracts: phase6-release-contracts
	bash scripts/e2e-phase6_contract_test.sh
	bash scripts/e2e-phase6_security_contract_test.sh
	bash scripts/e2e-phase6_resources_contract_test.sh
	bash scripts/phase6-docs_contract_test.sh
	bash scripts/phase6-ci_contract_test.sh
	bash scripts/phase6-ci_contract_mutation_test.sh
	bash scripts/e2e-artifact-sanitization_contract_test.sh

phase5-backup-contract:
	bash scripts/phase5-backup_contract_test.sh

phase5-backup-live:
	bash scripts/phase5-backup_live_test.sh

phase5-backup:
	bash scripts/phase5-backup.sh --project happylearn-dev --trigger $(BACKUP_TRIGGER)

phase5-restore-contract:
	bash scripts/phase5-restore_contract_test.sh

phase5-restore-live-contract:
	bash scripts/phase5-restore_live_contract_test.sh

phase5-failure-matrix-contract:
	bash scripts/e2e-phase5_failure_matrix_contract_test.sh

phase5-operations-docs-contract:
	bash scripts/phase5-operations-docs_contract_test.sh

host-sampler:
	@mkdir -p $(GOBIN)
	$(GO) build -o $(GOBIN)/host-sampler ./cmd/host-sampler

host-metrics-contract:
	bash scripts/host-metrics_contract_test.sh

host-metrics-uid-contract:
	bash scripts/host-metrics_uid_contract_test.sh

host-metrics-live:
	bash scripts/host-metrics_live_test.sh

host-metrics: host-sampler
	HOST_SAMPLER_BIN=$(GOBIN)/host-sampler bash scripts/collect-host-metrics.sh --environment development
