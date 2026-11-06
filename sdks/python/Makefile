.PHONY: ci ci-full test deps
deps:
	@test -x .venv/bin/python || python3 -m venv .venv
	.venv/bin/python -m pip install --quiet -e .
test: deps
	.venv/bin/python -m unittest discover -s tests
ci: test
ci-full: ci
