# tools/media — gravador de terminal, renderizador de GIF e os scripts do `make media`.
# Alvos padronizados do monorepo: lint, test, ci, clean (+ media, que regrava docs/media).
SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help
.SHELLFLAGS := -eu -o pipefail -c

export GOTOOLCHAIN ?= auto

.PHONY: help lint test ci ci-full media clean

help: ## lista os alvos
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

lint: ## gofmt, go vet e shellcheck dos scripts
	@out="$$(gofmt -l .)"; if [[ -n "$$out" ]]; then echo "gofmt: $$out"; exit 1; fi
	go vet ./...
	@if command -v shellcheck >/dev/null 2>&1; then shellcheck media.sh android.sh; else echo "shellcheck ausente: pulado"; fi

test: ## testes (com -race)
	go test -race -count=1 ./...

ci: lint test ## o que o pre-push roda

ci-full: ci

media: ## regrava docs/media (o mesmo que make media na raiz)
	./media.sh all

clean: ## nada a limpar (a pasta de trabalho é MEDIA_WORK, fora do repositório)
	@true
