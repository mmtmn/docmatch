# apps/ios — o app iOS (Swift 6, SwiftUI). Alvos exigidos pelo runner: setup, lint, test, ci,
# ci-full, clean (+ app, release). O DocmatchKit (tudo que não é tela) testa no Linux, num
# contêiner Swift, e no Mac direto; as telas compilam só no Mac (make app, ou o workflow ios).
SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

ROOT := $(abspath ../..)
# Imagem oficial do Swift para os testes no Linux (o mesmo compilador do Xcode 16.3+).
SWIFT_IMAGE ?= swift:6.1-noble
SWIFT_CACHE ?= $(HOME)/.cache/docmatch/swift-ios
ENV ?= prod
SIMULATOR ?= generic/platform=iOS Simulator

.PHONY: help setup tokens lint test ci ci-full app release clean

help: ## lista os alvos
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

setup: ## no Mac: xcodegen (Homebrew); no Linux: a imagem do Swift
	@if [[ "$$(uname)" == Darwin ]]; then command -v xcodegen >/dev/null || brew install xcodegen; \
	  else docker pull -q $(SWIFT_IMAGE); fi

tokens: ## confere se Tokens.swift/Tokens.kt batem com packages/design-tokens/tokens.json
	node $(ROOT)/tools/mobile/tokens.mjs --check

lint: tokens ## o compilador em modo estrito (Swift 6) é o lint; aqui, os tokens

test: ## testes do DocmatchKit (API, sessão, pré-checagem, fluxo, modelos das telas)
	@if [[ "$$(uname)" == Darwin ]]; then cd DocmatchKit && swift test; \
	  else command -v docker >/dev/null || { echo "docker ausente: os testes do DocmatchKit rodam no contêiner $(SWIFT_IMAGE)" >&2; exit 1; }; \
	  mkdir -p "$(SWIFT_CACHE)"; \
	  docker run --rm --user "$$(id -u):$$(id -g)" -e HOME=/tmp -v "$(ROOT):/repo" -v "$(SWIFT_CACHE):/cache" \
	    -w /repo/apps/ios/DocmatchKit $(SWIFT_IMAGE) swift test --scratch-path /cache/build; fi

ci: lint test ## tokens + testes do DocmatchKit (o que o pre-push roda)

ci-full: ci ## ci + o app inteiro, no Mac
	@if [[ "$$(uname)" == Darwin ]]; then $(MAKE) app; else echo "app: só no Mac (workflow ios no GitHub)"; fi

app: ## no Mac: gera o projeto e compila o app para o simulador, sem assinatura
	xcodegen generate --quiet
	xcodebuild -project Docmatch.xcodeproj -scheme Docmatch -configuration Debug -destination '$(SIMULATOR)' \
	  CODE_SIGNING_ALLOWED=NO build -quiet

release: ## no Mac: arquivo de release para a origem do ambiente (outputs do Terraform; DOCMATCH_API_BASE_URL sobrepõe)
	@url="$${DOCMATCH_API_BASE_URL:-$$(terraform -chdir=$(ROOT)/infra/terraform/envs/$(ENV) output -raw site_url)}"; \
	  [[ "$$url" == https://* ]] || { echo "sem origem HTTPS da API: aplique o ambiente $(ENV) ou defina DOCMATCH_API_BASE_URL" >&2; exit 1; }; \
	  [[ -n "$${APPLE_TEAM_ID:-}" ]] || { echo "APPLE_TEAM_ID vazio: é o time que assina o app" >&2; exit 1; }; \
	  auth=(); if [[ -n "$${APP_STORE_CONNECT_KEY_PATH:-}" ]]; then \
	    auth=(-authenticationKeyPath "$$APP_STORE_CONNECT_KEY_PATH" -authenticationKeyID "$$APP_STORE_CONNECT_KEY_ID" \
	      -authenticationKeyIssuerID "$$APP_STORE_CONNECT_ISSUER_ID"); fi; \
	  xcodegen generate --quiet && \
	  xcodebuild -project Docmatch.xcodeproj -scheme Docmatch -configuration Release -destination 'generic/platform=iOS' \
	    -archivePath build/Docmatch.xcarchive -allowProvisioningUpdates "$${auth[@]}" DEVELOPMENT_TEAM="$$APPLE_TEAM_ID" \
	    DOCMATCH_API_BASE_URL="$$url" DOCMATCH_GOOGLE_IOS_CLIENT_ID="$${DOCMATCH_GOOGLE_IOS_CLIENT_ID:-}" archive
	@echo "arquivo em build/Docmatch.xcarchive (envio: Xcode Organizer ou xcodebuild -exportArchive)"

clean: ## apaga o projeto gerado e os builds
	rm -rf Docmatch.xcodeproj build DocmatchKit/.build
