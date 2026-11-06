SHELL := /usr/bin/env bash
.DEFAULT_GOAL := ci
ROOT := $(abspath ../..)
PNPM ?= pnpm
PYTHON ?= python3

.PHONY: setup lint test build ci ci-full native-test safari-app e2e-local e2e-firefox clean
setup:
	cd $(ROOT) && $(PNPM) install --frozen-lockfile
lint:
	$(PNPM) run typecheck
	cd ../web && $(PNPM) exec prettier --check '../extensions/src' '../extensions/tools' '../extensions/*.json' '../extensions/*.html'
test:
	$(PNPM) run test
native-test:
	@if [[ "$$(uname)" == Darwin ]]; then swift test --package-path safari; \
	  else echo "Safari nativo: execute native-test em um Mac com Xcode."; fi
build:
	$(PNPM) run build:local
ci: lint test build native-test
ci-full: ci
	@if [[ "$$(uname)" == Darwin ]]; then $(MAKE) safari-app; fi
safari-app: build
	cd safari && xcodegen generate --quiet
	xcodebuild -project safari/DocmatchSafari.xcodeproj -scheme DocmatchSafari -configuration Debug \
	  -destination 'generic/platform=macOS' CODE_SIGNING_ALLOWED=NO build -quiet
e2e-local: build
	node tools/test-chrome.mjs
e2e-firefox: build
	$(PYTHON) tools/test-firefox.py
clean:
	rm -rf dist safari/Web safari/.build safari/DocmatchSafari.xcodeproj
