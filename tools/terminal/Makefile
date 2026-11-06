.PHONY: ci ci-full test
test:
	node --test test/*.test.mjs
	node --test ../dev/check-local.test.mjs
ci: test
	bash -n release.sh
	@if command -v shellcheck >/dev/null; then shellcheck release.sh; fi
ci-full: ci
