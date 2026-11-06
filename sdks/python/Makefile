.PHONY: ci ci-full test deps package
deps:
	@test -x .venv/bin/python || python3 -m venv .venv
	.venv/bin/python -m pip install --quiet -e .
test: deps
	.venv/bin/python -m unittest discover -s tests
ci: test
ci-full: ci
package: test
	.venv/bin/python -m pip install --quiet 'build>=1.2,<2' 'twine>=7,<8'
	.venv/bin/python -m build
	.venv/bin/python -m twine check --strict dist/*
