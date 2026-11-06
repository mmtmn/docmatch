# apps/android — o app Android (Kotlin, Jetpack Compose). Alvos exigidos pelo runner: setup, lint,
# test, ci, ci-full, clean (+ build, e2e-local, release). O SDK vem de local.properties (sdk.dir)
# ou de ANDROID_HOME; o núcleo (:core) é JVM puro e testa sem SDK nenhum.
SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

ROOT := $(abspath ../..)
GRADLE := ./gradlew --console=plain -q
ENV ?= prod
# Dispositivo gerenciado pelo Gradle (app/build.gradle.kts): emulador headless com KVM.
DEVICE ?= pixel6Api34
GPU ?= swiftshader_indirect
# Keep Gradle's generic Pixel AVD separate from the developer's running emulator.
TEST_ANDROID_HOME ?= $(ROOT)/.cache/android-test

.PHONY: help setup sdk-check tokens lint test ui-test build ci ci-full e2e-local release clean

help: ## lista os alvos
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

setup: sdk-check ## baixa o Gradle e as dependências (o SDK é do usuário: veja o README)
	$(GRADLE) help >/dev/null

sdk-check:
	@if [[ -z "$${ANDROID_HOME:-}" ]] && ! grep -qs '^sdk.dir=' local.properties; then \
	  echo "SDK do Android não encontrado: defina ANDROID_HOME ou sdk.dir em apps/android/local.properties (README)." >&2; exit 1; \
	fi

tokens: ## confere se Tokens.kt/Tokens.swift batem com packages/design-tokens/tokens.json
	node $(ROOT)/tools/mobile/tokens.mjs --check

test: ## testes do núcleo (API, sessão, textos, arquivos) na JVM
	$(GRADLE) -Pdocmatch.coreOnly=true :core:test

lint: sdk-check ## Android lint do app (avisos viram erro onde importa: abortOnError)
	$(GRADLE) :app:lintDebug

build: sdk-check ## APK de debug, APK dos testes instrumentados e o release minificado (R8)
	$(GRADLE) :app:assembleDebug :app:assembleDebugAndroidTest :app:assembleRelease -Pdocmatch.apiBaseUrl=https://api.docmatch.invalid

ui-test: sdk-check ## modelos de tela: troca de conta, retomada e conflitos
	$(GRADLE) :app:testDebugUnitTest

ci: tokens test ui-test lint build ## tokens + testes + lint + builds (o que o pre-push roda)

ci-full: ci e2e-local ## ci + o fluxo inteiro num emulador contra o make local

e2e-local: sdk-check ## make local + o app num emulador gerenciado: login local, gêmeo da Vantara, conta em dois aparelhos
	DM_LOCAL_LLM_MODE=off $(ROOT)/tools/dev/local.sh up
	mkdir -p "$(TEST_ANDROID_HOME)"
	ANDROID_USER_HOME="$(TEST_ANDROID_HOME)" ANDROID_EMULATOR_HOME="$(TEST_ANDROID_HOME)" \
	  ANDROID_AVD_HOME="$(TEST_ANDROID_HOME)/avd/gradle-managed" \
	  $(GRADLE) --no-daemon :app:$(DEVICE)DebugAndroidTest \
	  -Pandroid.testInstrumentationRunnerArguments.docmatchLocal=1 \
	  -Pandroid.testoptions.manageddevices.emulator.gpu=$(GPU)

release: sdk-check ## AAB de release para a origem do ambiente (outputs do Terraform; DOCMATCH_API_BASE_URL sobrepõe)
	@url="$${DOCMATCH_API_BASE_URL:-$$(terraform -chdir=$(ROOT)/infra/terraform/envs/$(ENV) output -raw site_url)}"; \
	  [[ -n "$$url" ]] || { echo "sem origem da API: aplique o ambiente $(ENV) ou defina DOCMATCH_API_BASE_URL" >&2; exit 1; }; \
	  google="$${GOOGLE_OAUTH_CLIENT_ID:-}"; \
	  [[ -n "$$google" ]] || echo "AVISO: GOOGLE_OAUTH_CLIENT_ID vazio; o release sai sem o botão do Google" >&2; \
	  echo "release para $$url"; \
	  $(GRADLE) :app:bundleRelease -Pdocmatch.apiBaseUrl="$$url" -Pdocmatch.googleServerClientId="$$google"
	@echo "AAB em app/build/outputs/bundle/release/ (assinatura: README)"

clean: ## apaga os builds
	$(GRADLE) clean
