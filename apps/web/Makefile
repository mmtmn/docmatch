# apps/web — site, blog e app (Astro + React). Alvos exigidos pelo runner: setup, lint, test, ci,
# ci-full, clean (+ typecheck, build, dev, e2e). Tudo instala no espaço do usuário; nada de sudo.
SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

ROOT := $(abspath ../..)
PNPM ?= pnpm
PNPM_VERSION := 11.5.3
export PATH := $(HOME)/.local/bin:$(PATH)
export PLAYWRIGHT_BROWSERS_PATH ?= $(HOME)/.cache/ms-playwright
E2E_PORT ?= 4173

.PHONY: help setup deps lint format typecheck test build ci ci-full dev preview e2e e2e-all lhci clean

help: ## lista os alvos
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

setup: ## pnpm (local ao usuário), dependências do workspace e o Chromium do Playwright
	@if ! command -v pnpm >/dev/null 2>&1; then \
	  echo "pnpm ausente: ativando via corepack em $(HOME)/.local/bin"; \
	  mkdir -p $(HOME)/.local/bin; \
	  corepack enable --install-directory $(HOME)/.local/bin; \
	  corepack prepare pnpm@$(PNPM_VERSION) --activate; \
	fi
	cd $(ROOT) && $(PNPM) install --frozen-lockfile
	@$(PNPM) exec playwright install chromium \
	  || echo "AVISO: não foi possível baixar o Chromium do Playwright; os testes e2e serão pulados."

deps: ## instala as dependências do workspace se ainda não estiverem instaladas (CI sem `setup`)
	@if [ ! -f $(ROOT)/node_modules/.modules.yaml ]; then \
	  echo "dependências ausentes: pnpm install --frozen-lockfile"; \
	  cd $(ROOT) && $(PNPM) install --frozen-lockfile; \
	fi

lint: deps ## eslint + prettier --check + verificação de chaves i18n
	$(PNPM) exec eslint .
	$(PNPM) exec prettier --check .
	node tools/i18n-check.mjs

format: ## prettier --write
	$(PNPM) exec prettier --write .

typecheck: deps ## astro check + tsc
	$(PNPM) exec astro check
	$(PNPM) exec tsc --noEmit -p tsconfig.json

test: deps ## testes unitários (vitest)
	$(PNPM) exec vitest run

build: deps ## build estático em dist/
	node tools/i18n-check.mjs
	$(PNPM) exec astro build

ci: lint typecheck test build ## portão rápido (< 6 min)

ci-full: ci e2e-all ## portão de release: ci + e2e em chromium, firefox e webkit (os que abrirem)

dev: ## servidor de desenvolvimento
	$(PNPM) exec astro dev

preview: ## serve dist/ como em produção
	$(PNPM) exec astro preview --host 127.0.0.1 --port $(E2E_PORT)

e2e: ## build com a tela de resultados ligada + Playwright (Chromium)
	PUBLIC_DM_FEATURE_RESULTS=true $(PNPM) exec astro build
	@browsers="$$(node tools/e2e-preflight.mjs chromium)"; \
	if [ -z "$$browsers" ]; then \
	  echo "AVISO: nenhum navegador do Playwright abre nesta máquina; e2e PULADO (rode: pnpm exec playwright install chromium)."; \
	else \
	  DM_E2E_BROWSERS="$$browsers" DM_WEB_E2E_PORT=$(E2E_PORT) $(PNPM) exec playwright test; \
	fi

e2e-all: ## como e2e, em todos os navegadores que abrirem (tenta baixar firefox e webkit)
	PUBLIC_DM_FEATURE_RESULTS=true $(PNPM) exec astro build
	@$(PNPM) exec playwright install firefox webkit >/dev/null 2>&1 \
	  || echo "AVISO: não foi possível baixar firefox/webkit; seguindo com os navegadores que abrirem."
	@browsers="$$(node tools/e2e-preflight.mjs chromium firefox webkit)"; \
	if [ -z "$$browsers" ]; then \
	  echo "AVISO: nenhum navegador do Playwright abre nesta máquina; e2e PULADO."; \
	else \
	  echo "e2e em: $$browsers"; \
	  DM_E2E_BROWSERS="$$browsers" DM_WEB_E2E_PORT=$(E2E_PORT) $(PNPM) exec playwright test; \
	fi

lhci: build ## Lighthouse CI com orçamentos (fora do ci)
	$(PNPM) exec lhci autorun --config=lighthouserc.cjs

clean: ## remove artefatos de build e de teste
	rm -rf dist .astro coverage playwright-report test-results .vitest .lighthouseci node_modules/.vite
