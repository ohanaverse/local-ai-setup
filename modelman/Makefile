SHELL := /bin/bash
.PHONY: help install test lint format typecheck check all clean

help:  ## Show this help.
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

install:  ## Install dev dependencies into the venv.
	uv sync

test:  ## Run the test suite.
	uv run pytest

lint:  ## Lint with ruff (no fixes).
	uv run ruff check src/ tests/

format:  ## Auto-format with ruff.
	uv run ruff format src/ tests/
	uv run ruff check --fix src/ tests/

typecheck:  ## Run mypy on the package.
	uv run mypy src/

check: lint typecheck  ## Lint + typecheck (no auto-fixes).

all: format test check  ## Format, run tests, then lint + typecheck.

clean:  ## Remove caches and build artifacts.
	rm -rf .pytest_cache .mypy_cache .ruff_cache htmlcov .coverage
	find . -type d -name __pycache__ -exec rm -rf {} +
