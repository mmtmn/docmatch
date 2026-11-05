# tools/installer — o instalador de uma linha (`docmatch`) e o servidor do site da imagem local.
#
# Alvos padronizados do monorepo: setup, lint, test, ci, clean (+ sync, build, cross, release).
# go:embed não lê fora do módulo: `make sync` copia para internal/assets os arquivos canônicos
# (textos, compose, tokens, gêmeo) e `make sync-check` (parte do ci) falha se a cópia divergir.
SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help
.SHELLFLAGS := -eu -o pipefail -c

ROOT := $(abspath ../..)
GOBIN ?= $(shell go env GOPATH)/bin
export GOBIN
export PATH := $(GOBIN):$(PATH)
export GOTOOLCHAIN ?= auto
export GOFLAGS ?= -mod=mod

GOLANGCI_VERSION := v2.15.0
ASSETS := internal/assets
CROSS_DIR ?= $(or $(TMPDIR),/tmp)/docmatch-installer-cross
TARGETS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: context help setup tools sync sync-check fmt vet lint scripts test ci ci-full build cross release-dry-run clean

help: ## lista os alvos
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

setup: ## instala o golangci-lint em $(GOBIN) e baixa os módulos
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	go mod download

tools: $(GOBIN)/golangci-lint

$(GOBIN)/golangci-lint:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

# ------------------------------------------------------------------ cópias embarcadas
sync: ## copia textos, compose, tokens e o gêmeo da Vantara para internal/assets
	cp $(ROOT)/packages/locales/pt-BR.json $(ASSETS)/locales/pt-BR.json
	cp $(ROOT)/deploy/local/compose.yaml $(ASSETS)/compose/compose.yaml
	cp $(ROOT)/packages/design-tokens/tokens.json $(ASSETS)/tokens/tokens.json
	rm -f $(ASSETS)/twin/*
	cp $(ROOT)/testdata/golden/vantara-twin/0[1-4]_* $(ROOT)/testdata/golden/vantara-twin/expected.yaml $(ASSETS)/twin/

sync-check: ## falha se alguma cópia embarcada divergir da original (rode make sync)
	@status=0; \
	for pair in packages/locales/pt-BR.json:locales/pt-BR.json deploy/local/compose.yaml:compose/compose.yaml \
	            packages/design-tokens/tokens.json:tokens/tokens.json; do \
	  src="$(ROOT)/$${pair%%:*}"; dst="$(ASSETS)/$${pair#*:}"; \
	  cmp -s "$$src" "$$dst" || { echo "$$dst divergiu de $${pair%%:*}: rode make -C tools/installer sync"; status=1; }; \
	done; \
	for f in $(ROOT)/testdata/golden/vantara-twin/0[1-4]_* $(ROOT)/testdata/golden/vantara-twin/expected.yaml; do \
	  cmp -s "$$f" "$(ASSETS)/twin/$$(basename $$f)" || { echo "$(ASSETS)/twin/$$(basename $$f) divergiu do gêmeo: rode make -C tools/installer sync"; status=1; }; \
	done; \
	exit $$status

# --------------------------------------------------------------------------- lint
fmt: ## gofmt em todo o módulo
	gofmt -w .

vet: ## go vet em Linux, macOS e Windows
	go vet ./...
	GOOS=darwin go vet ./...
	GOOS=windows go vet ./...

scripts: ## shellcheck e sh -n no install.sh e no release.sh; o install.ps1 no pwsh quando existe
	sh -n install.sh
	bash -n release.sh
	@if command -v shellcheck >/dev/null 2>&1; then shellcheck -s sh install.sh && shellcheck release.sh; \
	else echo "aviso: shellcheck não instalado; só sh -n"; fi
	@if command -v pwsh >/dev/null 2>&1; then \
	  pwsh -NoProfile -Command '$$e = $$null; [void][System.Management.Automation.Language.Parser]::ParseFile("$(CURDIR)/install.ps1", [ref]$$null, [ref]$$e); if ($$e) { $$e | ForEach-Object { $$_.ToString() }; exit 1 }'; \
	else echo "aviso: pwsh não instalado; install.ps1 sem checagem de sintaxe"; fi

lint: tools sync-check vet scripts ## gofmt -l, go vet (3 sistemas), golangci-lint, scripts e cópias
	@unformatted="$$(gofmt -l .)"; \
	  if [ -n "$$unformatted" ]; then echo "gofmt precisa rodar em:"; echo "$$unformatted"; exit 1; fi
	golangci-lint run ./...

# ------------------------------------------------------------------------- testes
test: ## unitários e telas (golden), com -race
	go test -race -count=1 ./...

cross: ## compila os seis alvos (CGO desligado) em CROSS_DIR e apaga
	@rm -rf $(CROSS_DIR) && mkdir -p $(CROSS_DIR)
	@for t in $(TARGETS); do \
	  os=$${t%/*}; arch=$${t#*/}; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '-s -w' -o $(CROSS_DIR)/docmatch-$$os-$$arch . || exit 1; \
	  echo "  $$os/$$arch ok"; \
	done
	@rm -rf $(CROSS_DIR)

ci: lint test cross context ## portão rápido: lint, testes, os seis binários e o contexto das imagens

context: ## o que o harness inclui de fora dele está no contexto da imagem (deploy/local)
	python3 -I ../ci/check-docker-context.py

ci-full: ci release-dry-run ## ci + o release em dry-run

# ------------------------------------------------------------------------ binários
build: ## bin/docmatch para esta máquina
	@mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o bin/ .

release-dry-run: ## release.sh --dry-run (compila, gera checksums e scripts; sem AWS)
	./release.sh --dry-run --out $(CURDIR)/dist/dry-run

clean: ## remove bin/ e dist/
	rm -rf bin dist
