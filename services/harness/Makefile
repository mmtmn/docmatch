# docmatch — harness Rust. Alvos chamados pelo Makefile da raiz e por tools/ci/run.sh.
# O target dir é compartilhado entre worktrees (regra 9): um só build do DuckDB por máquina.
SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

export CARGO_TARGET_DIR ?= $(HOME)/.cache/docmatch/cargo-target
export CARGO_TERM_COLOR ?= always
# Casos do proptest no portão rápido; `ci-full` sobe para 2000.
export PROPTEST_CASES ?= 256

CARGO ?= cargo
WORKSPACE_CRATES := dm-core dm-validators dm-normalize dm-ingest dm-match dm-graph dm-discover \
                    dm-guard dm-llm dm-sandbox dm-report dm-server dm-eval dm-media dm-tools \
                    dm-planner dm

.PHONY: help setup build lint fmt test deny audit ci ci-full clean dev serve release

help: ## lista os alvos
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

setup: ## instala ferramentas locais em ~/.cargo/bin (sem root)
	@command -v cargo-nextest >/dev/null 2>&1 || $(CARGO) install --locked cargo-nextest
	@command -v cargo-deny >/dev/null 2>&1 || $(CARGO) install --locked cargo-deny
	@command -v cargo-audit >/dev/null 2>&1 || $(CARGO) install --locked cargo-audit
	@mkdir -p "$(CARGO_TARGET_DIR)"
	@echo "harness: ferramentas prontas; target dir em $(CARGO_TARGET_DIR)"

build: ## compila o workspace inteiro (o DuckDB embutido demora na primeira vez)
	$(CARGO) build --workspace --all-targets

fmt: ## formata o código
	$(CARGO) fmt --all

lint: ## cargo fmt --check + clippy com -D warnings em todos os alvos
	$(CARGO) fmt --all -- --check
	$(CARGO) clippy --workspace --all-targets -- -D warnings

test: ## testes do workspace (cargo-nextest quando instalado)
	@if command -v cargo-nextest >/dev/null 2>&1; then \
	  $(CARGO) nextest run --workspace --no-fail-fast; \
	else \
	  echo "harness: aviso — cargo-nextest não instalado (make setup); usando cargo test" >&2; \
	  $(CARGO) test --workspace --no-fail-fast; \
	fi

deny: ## licenças, advisories, duplicatas e fontes (cargo-deny)
	@if command -v cargo-deny >/dev/null 2>&1; then $(CARGO) deny check; \
	else echo "harness: aviso — cargo-deny não instalado (make setup)" >&2; fi

audit: ## vulnerabilidades conhecidas no Cargo.lock (cargo-audit)
	@if command -v cargo-audit >/dev/null 2>&1; then $(CARGO) audit; \
	else echo "harness: aviso — cargo-audit não instalado (make setup)" >&2; fi

ci: lint test deny audit ## portão rápido (< 6 min com o target dir aquecido)

ci-full: ci ## portão de release: proptest com 2000 casos por propriedade
	PROPTEST_CASES=2000 $(MAKE) test

clean: ## remove só os artefatos dos crates deste workspace (o target dir é compartilhado)
	$(CARGO) clean $(foreach c,$(WORKSPACE_CRATES),-p $(c))

dev: ## compila e mostra a ajuda do CLI `dm`
	$(CARGO) run -p dm -- --help

serve: ## servidor gRPC do harness no socket de DM_HARNESS_SOCKET (fontes local:// em DM_MEDIA_LOCAL_DIR)
	cargo run -p dm-server --release

# dm-server estático para a instância (Graviton): musl ligado pelo rust-lld do próprio Rust;
# requer toolchain C cruzada para ring/TLS (aarch64-linux-musl-gcc ou CC/AR do target configurados).
# Sai como docmatch-harness, o nome da unidade systemd.
RELEASE_TARGET ?= aarch64-unknown-linux-musl
RELEASE_DIR ?= $(abspath ../../dist/release)
release: ## dm-server linux/arm64 estático em RELEASE_DIR como docmatch-harness (infra/scripts/deploy-app.sh)
	@rustup target list --installed | grep -qx '$(RELEASE_TARGET)' || rustup target add $(RELEASE_TARGET)
	CARGO_TARGET_AARCH64_UNKNOWN_LINUX_MUSL_LINKER=rust-lld CARGO_PROFILE_RELEASE_STRIP=symbols \
	  $(CARGO) build --release --locked -p dm-server --target $(RELEASE_TARGET)
	@mkdir -p $(RELEASE_DIR)
	install -m 0755 $(CARGO_TARGET_DIR)/$(RELEASE_TARGET)/release/dm-server $(RELEASE_DIR)/docmatch-harness
