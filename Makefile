# docmatch — alvos de alto nível. Cada subsistema tem o próprio Makefile com `ci`, `ci-full`,
# `test`, `lint` e `dev`; este arquivo só delega.
SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

SUBSYSTEMS := packages/contracts services/api services/harness apps/web apps/admin infra \
              sdks/python sdks/typescript sdks/go sdks/rust

.PHONY: help setup hooks dev ci ci-full lint test e2e clean local local-down e2e-local e2e-local-android

# Binário do harness usado pelo e2e (mesmo target dir do Makefile do harness).
HARNESS_TARGET ?= $(or $(CARGO_TARGET_DIR),$(HOME)/.cache/docmatch/cargo-target)

help: ## lista os alvos
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

hooks: ## ativa os git hooks versionados
	@tools/git-hooks/install.sh

setup: hooks ## instala ferramentas locais de cada subsistema
	@for s in $(SUBSYSTEMS); do \
	  if [ -f $$s/Makefile ] && make -C $$s -n setup >/dev/null 2>&1; then make -C $$s setup; fi; \
	done

dev: ## sobe o ambiente local (Postgres, MinIO, Mailpit, stripe-mock, modelo local) e os apps
	@if [ -f tools/dev/up.sh ]; then tools/dev/up.sh; else echo "tools/dev/up.sh ainda não existe"; fi

local: ## docmatch inteiro nesta máquina, sem chave nem login: Postgres, harness, API, worker e site
	tools/dev/local.sh up

local-down: ## para o que o make local subiu
	tools/dev/local.sh down

e2e-local: ## make local + Playwright no fluxo real (envio, estimativa, execução, resultados, conta)
	tools/dev/local.sh up
	cd apps/web && pnpm exec playwright test -c playwright.local.config.ts

e2e-local-android: ## make local + o app Android num emulador (login local, gêmeo da Vantara, conta em dois aparelhos)
	$(MAKE) -C apps/android e2e-local

ci: ## portão completo antes de push (< 6 min)
	@tools/ci/run.sh ci

ci-full: ## portão de release: e2e, carga, fuzz, scanners de segurança
	@tools/ci/run.sh ci-full

lint: ## só os lints
	@tools/lint/hardcode.sh
	@for s in $(SUBSYSTEMS); do \
	  if [ -f $$s/Makefile ] && make -C $$s -n lint >/dev/null 2>&1; then make -C $$s lint || exit 1; fi; \
	done

test: ## só os testes
	@for s in $(SUBSYSTEMS); do \
	  if [ -f $$s/Makefile ] && make -C $$s -n test >/dev/null 2>&1; then make -C $$s test || exit 1; fi; \
	done

e2e: ## API e harness reais: compila o dm-server e roda o pipeline da API contra ele (Docker para o Postgres)
	cd services/harness && cargo build -p dm-server
	cd services/api && DM_HARNESS_BIN=$(HARNESS_TARGET)/debug/dm-server \
	  go test -race -tags integration -count=1 -run TestPipelineWithRealHarness -v ./internal/runs/wire/

clean: ## limpa artefatos de build
	@for s in $(SUBSYSTEMS); do \
	  if [ -f $$s/Makefile ] && make -C $$s -n clean >/dev/null 2>&1; then make -C $$s clean; fi; \
	done
