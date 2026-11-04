# services/api — API, worker e migrador em Go.
#
# Alvos padronizados do monorepo: setup, lint, test, ci (< 6 min), ci-full, dev, clean.
# Ferramentas são instaladas só em $(GOBIN) (sem sudo). O `go` do sistema baixa a
# toolchain declarada no go.mod (GOTOOLCHAIN=auto).
SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help
.SHELLFLAGS := -eu -o pipefail -c

ROOT := $(abspath ../..)
GOBIN ?= $(shell go env GOPATH)/bin
export GOBIN
export PATH := $(GOBIN):$(PATH)
export GOTOOLCHAIN ?= auto
export GOFLAGS ?= -mod=mod
export CGO_ENABLED ?= 1
# Builds Rust de outros subsistemas compartilham um target dir; exportado aqui
# para que `make dev` a partir desta pasta não crie outro.
export CARGO_TARGET_DIR ?= $(HOME)/.cache/docmatch/cargo-target

SQLC_VERSION        := v1.31.1
GOLANGCI_VERSION    := v2.15.0
GOVULNCHECK_VERSION := v1.9.0
GOTESTSUM_VERSION   := v1.13.0

MODULE   := github.com/mmtmn/docmatch/services/api
GIT_SHA  := $(shell git -C $(ROOT) rev-parse --short HEAD 2>/dev/null || echo desconhecido)
BUILT_AT := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION  ?= dev
LDFLAGS  := -s -w \
  -X $(MODULE)/internal/version.GitSHA=$(GIT_SHA) \
  -X $(MODULE)/internal/version.BuiltAt=$(BUILT_AT) \
  -X $(MODULE)/internal/version.Version=$(VERSION)

COVER_DIR := coverage
# Pisos de cobertura (make test): 85% em internal/ e 95% nos pacotes centrais.
# Pacotes que só existem para falar com o banco (internal/db, pgstore, sqlc) e
# o harness de testes são medidos no ci-full, com os testes de integração.
UNIT_EXCLUDES := -exclude internal/db -exclude internal/db/pgstore -exclude internal/db/sqlc -exclude internal/testdb \
                 -exclude internal/account/accountpg -exclude internal/email/emailpg -exclude internal/session/sessionpg
FULL_EXCLUDES := -exclude internal/db/sqlc -exclude internal/testdb
FLOORS        := -floor 85 -pkg internal/credits=95 -pkg internal/config=95 -pkg internal/auth=90 -pkg internal/session=90

.PHONY: help setup tools generate sync sync-check sqlc-check fmt lint vet test test-integration \
        vuln ci ci-full build dev migrate run-api run-worker clean

help: ## lista os alvos
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# ----------------------------------------------------------------- ferramentas
setup: ## instala sqlc, golangci-lint, govulncheck e gotestsum em $(GOBIN)
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	go install gotest.tools/gotestsum@$(GOTESTSUM_VERSION)
	go mod download

# Instala só o que falta (usado por lint/test para funcionar em um clone novo).
tools: $(GOBIN)/golangci-lint $(GOBIN)/govulncheck $(GOBIN)/gotestsum

$(GOBIN)/sqlc:
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
$(GOBIN)/golangci-lint:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
$(GOBIN)/govulncheck:
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
$(GOBIN)/gotestsum:
	go install gotest.tools/gotestsum@$(GOTESTSUM_VERSION)

# ------------------------------------------------------------------- geração
sync: ## copia limits.yaml e db/migrations para as cópias embarcadas (go:embed)
	cp $(ROOT)/packages/contracts/limits.yaml internal/limits/limits.yaml
	rm -f internal/db/migrations/sql/*.sql
	cp $(ROOT)/db/migrations/*.sql internal/db/migrations/sql/

generate: sync $(GOBIN)/sqlc ## sync + regenera internal/db/sqlc a partir de db/queries
	cd $(ROOT)/db && sqlc generate
	gofmt -w internal/db/sqlc

sync-check: ## falha se as cópias embarcadas divergirem dos arquivos canônicos
	@diff -q $(ROOT)/packages/contracts/limits.yaml internal/limits/limits.yaml >/dev/null \
	  || { echo "internal/limits/limits.yaml divergiu de packages/contracts/limits.yaml: rode make generate"; exit 1; }
	@diff -qr $(ROOT)/db/migrations internal/db/migrations/sql >/dev/null \
	  || { echo "internal/db/migrations/sql divergiu de db/migrations: rode make generate"; exit 1; }

sqlc-check: ## falha se o código gerado estiver desatualizado (pula se o sqlc não estiver instalado)
	@if command -v sqlc >/dev/null 2>&1; then \
	  (cd $(ROOT)/db && sqlc diff) || { echo "internal/db/sqlc desatualizado: rode make generate"; exit 1; }; \
	else echo "aviso: sqlc não instalado (make setup); pulando a checagem do código gerado"; fi

# --------------------------------------------------------------------- lint
fmt: ## gofmt em todo o módulo
	gofmt -w .

vet: ## go vet nas duas combinações de tags
	go vet ./...
	go vet -tags integration ./...

lint: tools sync-check vet ## gofmt -l, go vet, golangci-lint (gosec, errcheck, staticcheck...) e deriva do sqlc
	@unformatted="$$(gofmt -l .)"; \
	  if [ -n "$$unformatted" ]; then echo "gofmt precisa rodar em:"; echo "$$unformatted"; exit 1; fi
	golangci-lint run ./...
	golangci-lint run --build-tags integration ./...
	@$(MAKE) --no-print-directory sqlc-check

# -------------------------------------------------------------------- testes
test: tools ## unitários: go test -race -short com piso de cobertura (85% / 95%)
	@mkdir -p $(COVER_DIR)
	gotestsum --format pkgname -- -race -short -count=1 -covermode=atomic \
	  -coverpkg=./internal/... -coverprofile=$(COVER_DIR)/unit.out ./...
	go run ./tools/covercheck -profile $(COVER_DIR)/unit.out $(UNIT_EXCLUDES) $(FLOORS)

test-integration: tools ## integração com testcontainers (Docker) + orçamento de EXPLAIN com ~100k linhas
	@mkdir -p $(COVER_DIR)
	gotestsum --format pkgname -- -race -tags integration -count=1 -covermode=atomic \
	  -coverpkg=./internal/... -coverprofile=$(COVER_DIR)/integration.out -timeout 20m ./...
	go run ./tools/covercheck -profile $(COVER_DIR)/integration.out $(FULL_EXCLUDES) $(FLOORS)

vuln: tools ## govulncheck ./...
	govulncheck ./...

ci: lint test vuln ## portão rápido (< 6 min): lint + unitários + govulncheck

ci-full: ci test-integration ## portão de release: ci + integração + orçamento de EXPLAIN

# ---------------------------------------------------------------- execução
build: ## compila api, worker e migrate em bin/ com git sha nos ldflags
	@mkdir -p bin
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/ ./cmd/...

# tools/dev/env.sh lê o .env com aspas seguras (valores com espaços e "<>").
LOAD_ENV := eval "$$($(ROOT)/tools/dev/env.sh $(ROOT)/.env)"

migrate: ## aplica as migrações (lê $(ROOT)/.env)
	@$(LOAD_ENV); go run ./cmd/migrate up

run-api: ## roda a API (lê $(ROOT)/.env)
	@$(LOAD_ENV); go run -ldflags '$(LDFLAGS)' ./cmd/api

run-worker: ## roda o worker (lê $(ROOT)/.env)
	@$(LOAD_ENV); go run -ldflags '$(LDFLAGS)' ./cmd/worker

dev: migrate ## migra e sobe API + worker juntos (Ctrl-C encerra os dois)
	@$(LOAD_ENV); \
	  trap 'kill 0' INT TERM; \
	  go run -ldflags '$(LDFLAGS)' ./cmd/api & \
	  go run -ldflags '$(LDFLAGS)' ./cmd/worker & \
	  wait

clean: ## remove binários e relatórios de cobertura
	rm -rf bin $(COVER_DIR)
