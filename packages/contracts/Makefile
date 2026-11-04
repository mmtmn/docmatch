# packages/contracts — contratos (OpenAPI, proto, esquemas JSON, limites, erros), tokens de
# design e geração de código. Chamado pelo Makefile da raiz e por tools/ci/run.sh.
SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../..)
CODEGEN := $(ROOT)/tools/codegen
TOKENS := $(ROOT)/packages/design-tokens
NODE_MODULES_STAMP := $(CODEGEN)/node_modules/.modules.yaml

# pnpm vem do PATH do usuário; sem ele, o corepack do Node 22 baixa a versão fixada em
# tools/codegen/package.json (packageManager) sem precisar de root.
PNPM := $(shell command -v pnpm 2>/dev/null)
ifeq ($(PNPM),)
  PNPM := corepack pnpm
endif

.PHONY: help setup deps gen check validate smoke lint test ci ci-full clean

help: ## lista os alvos
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

setup: ## instala as dependências do tools/codegen (pnpm, user-local, sem root)
	@cd $(CODEGEN) && $(PNPM) install --frozen-lockfile

$(NODE_MODULES_STAMP): $(CODEGEN)/package.json $(CODEGEN)/pnpm-lock.yaml
	@cd $(CODEGEN) && $(PNPM) install --frozen-lockfile
	@touch $@

deps: $(NODE_MODULES_STAMP)

gen: deps ## regenera packages/contracts/gen/* e packages/design-tokens/tokens.css
	@node $(CODEGEN)/codegen.mjs gen
	@node $(TOKENS)/build.mjs

check: deps ## falha se algum arquivo gerado estiver desatualizado
	@node $(CODEGEN)/codegen.mjs check
	@node $(TOKENS)/build.mjs --check

validate: deps ## OpenAPI (público e admin), esquemas + exemplos, cruzamentos com limits/errors, buf lint
	@node $(CODEGEN)/codegen.mjs validate

smoke: deps ## compila os arquivos gerados com as toolchains instaladas (go, rustc, node)
	@node $(CODEGEN)/codegen.mjs smoke

lint: validate ## validação completa dos contratos

test: deps ## exemplos dos esquemas, codegen check, contraste AA dos tokens, compilação e testes unitários
	@node $(CODEGEN)/codegen.mjs check
	@node $(TOKENS)/build.mjs --check
	@node $(CODEGEN)/codegen.mjs smoke
	@cd $(CODEGEN) && node --test 'test/**/*.test.mjs'

ci: lint test ## portão rápido (< 1 min)

ci-full: ci ## portão de release (igual ao ci: nada aqui é lento)

clean: ## remove dependências instaladas
	@rm -rf $(CODEGEN)/node_modules
