.PHONY: ci ci-full test
test:
	pnpm build
	pnpm test
ci: test
	node ../tools/generate.mjs --check
ci-full: ci
