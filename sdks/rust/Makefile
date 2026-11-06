.PHONY: ci ci-full test
export CARGO_TARGET_DIR ?= $(HOME)/.cache/docmatch/sdk-rust-target
test:
	cargo test --locked
ci: test
	cargo fmt --check
	cargo clippy --all-targets --locked -- -D warnings
ci-full: ci
