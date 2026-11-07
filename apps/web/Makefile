# apps/web — site, blog e app (Astro + React). Alvos exigidos pelo runner: setup, lint, test, ci,
# ci-full, clean (+ typecheck, build, dev, e2e). Tudo instala no espaço do usuário; nada de sudo.
SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

ROOT := $(abspath ../..)
PNPM ?= pnpm
PNPM_VERSION := 11.5.3
export PATH := $(HOME)/.local/bin:$(HOME)/.cargo/bin:$(PATH)
export PLAYWRIGHT_BROWSERS_PATH ?= $(HOME)/.cache/ms-playwright
# Port of `make preview`. The e2e targets pick a free port per run (tools/free-port.mjs) so parallel
# worktrees never attach to each other's servers; set E2E_PORT to force one.
PREVIEW_PORT ?= 4173
E2E_HOST ?= 127.0.0.1

.PHONY: help setup deps sync lint format typecheck test build ci ci-full dev preview e2e e2e-all lhci clean

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
	@if ! command -v mdbook >/dev/null 2>&1 || [ "$$(mdbook --version)" != 'mdbook v0.5.4' ]; then \
	  cargo install mdbook --version 0.5.4 --locked; \
	fi
	@$(PNPM) exec playwright install chromium \
	  || echo "AVISO: não foi possível baixar o Chromium do Playwright; os testes e2e serão pulados."

deps: ## instala as dependências do workspace se ainda não estiverem instaladas (CI sem `setup`)
	@if [ ! -f $(ROOT)/node_modules/.modules.yaml ]; then \
	  echo "dependências ausentes: pnpm install --frozen-lockfile"; \
	  cd $(ROOT) && $(PNPM) install --frozen-lockfile; \
	fi

sync: deps ## gera .astro/ (tipos de astro:env, astro:content e das rotas) antes de lint e typecheck
	$(PNPM) exec astro sync

# The type-aware ESLint rules need the generated `.astro/` directory: without it `astro:env/*`
# resolves to `error` types and no-unsafe-* fails on a fresh checkout. `sync` runs first.
lint: sync ## eslint + prettier --check + verificação de chaves i18n
	$(PNPM) exec eslint .
	$(PNPM) exec prettier --check .
	node tools/i18n-check.mjs

format: ## prettier --write
	$(PNPM) exec prettier --write .

typecheck: sync ## astro check + tsc
	$(PNPM) exec astro check
	$(PNPM) exec tsc --noEmit -p tsconfig.json

test: deps ## testes unitários (vitest)
	$(PNPM) exec vitest run

build: deps ## build estático em dist/
	node tools/i18n-check.mjs
	$(PNPM) exec astro build

ci: lint typecheck test build ## portão rápido (< 6 min)

ci-full: ci e2e-all ## portão de release: ci + e2e em chromium, firefox e webkit

dev: ## servidor de desenvolvimento
	$(PNPM) exec astro dev

# `astro preview` is never used here: Astro 7 daemonizes it when it detects an agent or a non-TTY
# (and `astro preview stop` is then needed), which left orphan servers behind test runs.
# tools/serve-dist.mjs stays in the foreground and maps clean URLs exactly like the CloudFront
# function in docs/headers.md, so what the suite exercises is what production serves.
preview: ## serve dist/ como em produção (URLs limpas como no CloudFront)
	node tools/serve-dist.mjs --host $(E2E_HOST) --port $(PREVIEW_PORT)

# Playwright starts (and stops) tools/serve-dist.mjs itself on a port chosen for this run;
# reuseExistingServer is off, so a foreign server on the port fails fast instead of being tested.
define run_e2e
@set -e; browsers="$$(node tools/e2e-preflight.mjs $(1))"; \
expected="$$(echo '$(1)' | tr ' ' ',')"; \
[[ "$$browsers" == "$$expected" ]] || { echo "Faltam navegadores: esperado $$expected, disponível $$browsers" >&2; exit 1; }; \
tls_dir="$$(mktemp -d)"; trap 'rm -rf "$$tls_dir"' EXIT; \
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=localhost \
  -addext 'subjectAltName=DNS:localhost,IP:127.0.0.1' \
  -keyout "$$tls_dir/key.pem" -out "$$tls_dir/cert.pem" >/dev/null 2>&1; \
port="$${E2E_PORT:-$$(node tools/free-port.mjs)}"; \
echo "e2e em: $$browsers (https://$(E2E_HOST):$$port)"; \
DM_E2E_TLS_CERT="$$tls_dir/cert.pem" DM_E2E_TLS_KEY="$$tls_dir/key.pem" \
  DM_E2E_SUITE=$(2) DM_E2E_BROWSERS="$$browsers" DM_WEB_E2E_HOST=$(E2E_HOST) DM_WEB_E2E_PORT=$$port $(PNPM) exec playwright test
endef

e2e: ## build com a tela de resultados ligada + Playwright (Chromium)
	DM_PUBLIC_BASE_URL=https://docmatch.example.invalid PUBLIC_DM_API_BASE_URL= PUBLIC_DM_FEATURE_RESULTS=true $(PNPM) exec astro build
	$(call run_e2e,chromium,demo)
	DM_PUBLIC_BASE_URL=https://docmatch.example.invalid PUBLIC_DM_API_BASE_URL=https://docmatch-api.example.invalid PUBLIC_DM_FEATURE_RESULTS=true $(PNPM) exec astro build
	$(call run_e2e,chromium,live)

e2e-all: ## como e2e, exige Chromium, Firefox e WebKit (certificado local temporário)
	@$(PNPM) exec playwright install chromium firefox webkit
	DM_PUBLIC_BASE_URL=https://docmatch.example.invalid PUBLIC_DM_API_BASE_URL= PUBLIC_DM_FEATURE_RESULTS=true $(PNPM) exec astro build
	$(call run_e2e,chromium firefox webkit,demo)
	DM_PUBLIC_BASE_URL=https://docmatch.example.invalid PUBLIC_DM_API_BASE_URL=https://docmatch-api.example.invalid PUBLIC_DM_FEATURE_RESULTS=true $(PNPM) exec astro build
	$(call run_e2e,chromium firefox webkit,live)

lhci: build ## Lighthouse CI com orçamentos (fora do ci)
	$(PNPM) exec lhci autorun --config=lighthouserc.cjs

clean: ## remove artefatos de build e de teste (e para um `astro preview` antigo deste diretório)
	@if [ -f .astro/preview.json ] && [ -f $(ROOT)/node_modules/.modules.yaml ]; then \
	  $(PNPM) exec astro preview stop >/dev/null 2>&1 || true; \
	fi
	rm -rf dist .astro coverage playwright-report test-results .vitest .lighthouseci node_modules/.vite
