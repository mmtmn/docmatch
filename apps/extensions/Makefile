SHELL := /usr/bin/env bash
.DEFAULT_GOAL := ci
ROOT := $(abspath ../..)
PNPM ?= pnpm

.PHONY: setup lint test build ci ci-full clean
setup:
	cd $(ROOT) && $(PNPM) install --frozen-lockfile
lint:
	$(PNPM) run typecheck
	cd ../web && $(PNPM) exec prettier --check '../extensions/src' '../extensions/tools' '../extensions/*.json' '../extensions/*.html'
test:
	$(PNPM) run test
build:
	$(PNPM) run build:local
ci: lint test build
ci-full: ci
clean:
	rm -rf dist
