# infra — Terraform, Packer e Ansible. Alvos obrigatórios do monorepo: setup, lint, test, ci,
# ci-full, clean (+ validate). Nenhum alvo deste arquivo toca a conta AWS, exceto os alvos
# explícitos init-<env>/plan-<env>/apply-<env>, que o operador roda à mão.
# /bin/bash (not "env bash"): with .ONESHELL make executes SHELL directly, without a PATH lookup.
SHELL := /bin/bash
.DEFAULT_GOAL := help
.ONESHELL:
.SHELLFLAGS := -eu -o pipefail -c

INFRA_DIR  := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
REPO_ROOT  := $(abspath $(INFRA_DIR)/..)
LOCAL_BIN  := $(HOME)/.local/bin
VENV_DIR   := $(HOME)/.local/share/docmatch/ansible-venv
CACHE_DIR  := $(HOME)/.cache/docmatch

# Versões fixadas (ver infra/README.md para a política de atualização).
TFLINT_VERSION        := 0.65.0
PACKER_VERSION        := 1.16.2
ANSIBLE_CORE_VERSION  := 2.21.6
ANSIBLE_LINT_VERSION  := 26.10.1
# shellcheck-py empacota o binário do shellcheck 0.11.0 como wheel (sem root, mesmo venv).
SHELLCHECK_PY_VERSION := 0.11.0.1

GOARCH := $(shell uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')

# Um único cache de providers para todos os worktrees (o provider aws passa de 600 MB).
export TF_PLUGIN_CACHE_DIR := $(CACHE_DIR)/terraform-plugins
export TF_IN_AUTOMATION    := 1
export TF_INPUT            := 0
export TFLINT_CONFIG_FILE  := $(INFRA_DIR)/.tflint.hcl
export ANSIBLE_CONFIG      := $(INFRA_DIR)/ansible/ansible.cfg
export PATH                := $(LOCAL_BIN):$(VENV_DIR)/bin:$(PATH)

TF_ENVS    := $(wildcard $(INFRA_DIR)/terraform/envs/*)
TF_MODULES := $(wildcard $(INFRA_DIR)/terraform/modules/*)
TF_ROOTS   := $(TF_ENVS) $(TF_MODULES)

.PHONY: help setup setup-tflint setup-packer setup-ansible lint lint-terraform lint-tflint \
        lint-ansible lint-packer lint-shell validate validate-terraform validate-packer \
        test trivy ci ci-full clean init-% plan-% apply-% packer-build

help: ## lista os alvos
	@grep -E '^[a-zA-Z_%-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# ------------------------------------------------------------------------------------- setup
setup: setup-tflint setup-packer setup-ansible ## instala tflint, packer, ansible e ansible-lint sem root
	@mkdir -p "$(TF_PLUGIN_CACHE_DIR)"
	@command -v terraform >/dev/null || echo "aviso: terraform não encontrado; instale >= 1.17 em $(LOCAL_BIN)"
	@command -v trivy >/dev/null || echo "aviso: trivy não encontrado; o alvo test vai falhar"
	@echo "setup concluído"

setup-tflint: ## tflint $(TFLINT_VERSION) em ~/.local/bin, com verificação de checksum
	@if tflint --version 2>/dev/null | grep -q "version $(TFLINT_VERSION)"; then echo "tflint $(TFLINT_VERSION) já instalado"; exit 0; fi
	tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT
	base="https://github.com/terraform-linters/tflint/releases/download/v$(TFLINT_VERSION)"
	curl -fsSL -o "$$tmp/tflint.zip" "$$base/tflint_linux_$(GOARCH).zip"
	curl -fsSL -o "$$tmp/checksums.txt" "$$base/checksums.txt"
	(cd "$$tmp" && grep " tflint_linux_$(GOARCH).zip$$" checksums.txt | sed 's/tflint_linux_$(GOARCH).zip/tflint.zip/' | sha256sum -c -)
	mkdir -p "$(LOCAL_BIN)" && unzip -o -q "$$tmp/tflint.zip" -d "$$tmp" && install -m 0755 "$$tmp/tflint" "$(LOCAL_BIN)/tflint"
	tflint --version
	@tflint --init >/dev/null && echo "plugins do tflint instalados"

setup-packer: ## packer $(PACKER_VERSION) em ~/.local/bin, com verificação de checksum
	@if packer version 2>/dev/null | grep -q "v$(PACKER_VERSION)"; then echo "packer $(PACKER_VERSION) já instalado"; exit 0; fi
	tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT
	base="https://releases.hashicorp.com/packer/$(PACKER_VERSION)"
	curl -fsSL -o "$$tmp/packer.zip" "$$base/packer_$(PACKER_VERSION)_linux_$(GOARCH).zip"
	curl -fsSL -o "$$tmp/SHA256SUMS" "$$base/packer_$(PACKER_VERSION)_SHA256SUMS"
	(cd "$$tmp" && grep " packer_$(PACKER_VERSION)_linux_$(GOARCH).zip$$" SHA256SUMS | sed 's/packer_$(PACKER_VERSION)_linux_$(GOARCH).zip/packer.zip/' | sha256sum -c -)
	mkdir -p "$(LOCAL_BIN)" && unzip -o -q "$$tmp/packer.zip" -d "$$tmp" && install -m 0755 "$$tmp/packer" "$(LOCAL_BIN)/packer"
	packer version

setup-ansible: ## ansible-core, ansible-lint e shellcheck em um venv do usuário (sem root) e as collections fixadas
	@if [ ! -x "$(VENV_DIR)/bin/pip" ]; then python3 -m venv "$(VENV_DIR)"; fi
	"$(VENV_DIR)/bin/pip" install --quiet --disable-pip-version-check \
	  "ansible-core==$(ANSIBLE_CORE_VERSION)" "ansible-lint==$(ANSIBLE_LINT_VERSION)" \
	  "shellcheck-py==$(SHELLCHECK_PY_VERSION)" passlib
	mkdir -p "$(LOCAL_BIN)"
	for b in ansible ansible-playbook ansible-galaxy ansible-lint ansible-vault shellcheck; do ln -sfn "$(VENV_DIR)/bin/$$b" "$(LOCAL_BIN)/$$b"; done
	"$(VENV_DIR)/bin/ansible-galaxy" collection install -r "$(INFRA_DIR)/ansible/requirements.yml" >/dev/null
	"$(VENV_DIR)/bin/ansible" --version | head -1
	"$(VENV_DIR)/bin/ansible-lint" --version | head -1
	"$(VENV_DIR)/bin/shellcheck" --version | sed -n 2p

# -------------------------------------------------------------------------------------- lint
lint: lint-terraform lint-tflint lint-ansible lint-packer lint-shell ## todos os lints

lint-terraform: ## terraform fmt -check -recursive
	@terraform fmt -check -recursive -diff "$(INFRA_DIR)/terraform"

lint-tflint: ## tflint --recursive (ruleset terraform + aws)
	@cd "$(INFRA_DIR)/terraform" && tflint --init >/dev/null && tflint --recursive --format compact

lint-ansible: ## ansible-lint no playbook e no papel hardening
	@cd "$(INFRA_DIR)/ansible" && ansible-lint --offline --strict

lint-packer: ## packer fmt -check
	@packer fmt -check -recursive "$(INFRA_DIR)/packer"

lint-shell: ## shellcheck nos scripts (instalado pelo setup via shellcheck-py; nunca pula)
	@command -v shellcheck >/dev/null || { echo "erro: shellcheck não encontrado; rode make -C infra setup-ansible"; exit 1; }
	@shellcheck -x "$(INFRA_DIR)"/scripts/*.sh "$(INFRA_DIR)"/packer/scripts/*.sh "$(INFRA_DIR)"/ansible/roles/hardening/files/*.sh

# ---------------------------------------------------------------------------------- validate
validate: validate-terraform validate-packer ## terraform validate em cada env e módulo + packer validate

validate-terraform: ## terraform init -backend=false && terraform validate em cada raiz
	@mkdir -p "$(TF_PLUGIN_CACHE_DIR)"
	@status=0
	for d in $(TF_ROOTS); do
	  printf '%-60s' "$${d#$(REPO_ROOT)/}"
	  if (cd "$$d" && terraform init -backend=false -input=false -no-color >/dev/null 2>"$$d/.init.err" && terraform validate -no-color >/dev/null 2>"$$d/.validate.err"); then
	    echo "ok"; rm -f "$$d/.init.err" "$$d/.validate.err"
	  else
	    echo "FALHOU"; cat "$$d/.init.err" "$$d/.validate.err" 2>/dev/null; status=1
	  fi
	done
	exit $$status

validate-packer: ## packer init + packer validate com o arquivo de exemplo de variáveis
	@cd "$(INFRA_DIR)/packer" && packer init . >/dev/null && packer validate -var-file=example.pkrvars.hcl .

# -------------------------------------------------------------------------------------- test
test: validate trivy ## validate + trivy config com a linha de base de achados aceitos

trivy: ## trivy config (misconfigurações em Terraform) com .trivyignore.yaml como linha de base
	@cd "$(INFRA_DIR)" && trivy config --quiet --exit-code 1 --severity LOW,MEDIUM,HIGH,CRITICAL \
	  --ignorefile "$(INFRA_DIR)/.trivyignore.yaml" --skip-dirs '**/.terraform' "$(INFRA_DIR)/terraform"

ci: lint test ## portão rápido (< 6 min)

ci-full: ci ## portão de release (igual ao ci neste subsistema)

clean: ## remove .terraform, manifestos do packer e caches locais
	@find "$(INFRA_DIR)/terraform" -type d -name .terraform -prune -exec rm -rf {} +
	@find "$(INFRA_DIR)/terraform" -type f \( -name '.init.err' -o -name '.validate.err' -o -name '*.tfplan' \) -delete
	@rm -rf "$(INFRA_DIR)/packer/manifest.json" "$(INFRA_DIR)/packer/packer_cache" "$(INFRA_DIR)/ansible/.ansible-lint-cache"

# ---------------------------------------------------------------- operação (nunca no CI)
init-%: ## terraform init no env (ex.: make init-dev); exige envs/<env>/backend.hcl
	@cd "$(INFRA_DIR)/terraform/envs/$*" && test -f backend.hcl || { echo "falta backend.hcl: rode infra/scripts/bootstrap-state.sh"; exit 1; }
	@cd "$(INFRA_DIR)/terraform/envs/$*" && terraform init -backend-config=backend.hcl -input=false

plan-%: ## terraform plan no env (ex.: make plan-prod)
	@cd "$(INFRA_DIR)/terraform/envs/$*" && terraform plan -input=false -out="$*.tfplan"

apply-%: ## terraform apply do plano salvo (ex.: make apply-prod)
	@cd "$(INFRA_DIR)/terraform/envs/$*" && terraform apply -input=false "$*.tfplan"

packer-build: ## constrói a AMI endurecida (exige PKR_VAR_subnet_id e PKR_VAR_iam_instance_profile)
	@cd "$(INFRA_DIR)/packer" && packer init . && packer build .
