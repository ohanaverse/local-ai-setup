SHELL := /bin/bash
.PHONY: help install test lint format-check format check clean

help:  ## Show this help.
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

install:  ## Install dev dependencies into the venv.
	uv sync

test:  ## Run the test suite.
	uv run pytest

lint:  ## Lint with ruff (no fixes).
	uv run ruff check .

format-check:  ## Verify ruff formatting (no changes).
	uv run ruff format --check .

format:  ## Auto-format with ruff.
	uv run ruff format .
	uv run ruff check --fix .

check: lint format-check  ## Lint + format check (no auto-fixes).

clean:  ## Remove caches.
	rm -rf .pytest_cache .ruff_cache
	find . -type d -name __pycache__ -not -path './.venv/*' -exec rm -rf {} +
