.PHONY: lint lint-shell check-links test-all install

SHELL_SCRIPTS := \
	bin/llm-isolate-provider \
	bin/llm-restore-providers \
	bin/lib/poll.sh \
	bin/lib/mlx-lm-resolve.sh \
	bin/lib/mlx-lm-server.sh \
	bin/lib/mtplx.sh \
	bin/mlx-quantize \
	benchmarks/qwen3.8-benchmark \
	benchmarks/qwen3.8-benchmark-multi \
	benchmarks/ornith-1.5-benchmark \
	benchmarks/ornith-1.5-benchmark-multi \
	benchmarks/lib/benchmark-common.sh \
	benchmarks/lib/benchmark-multi.sh \
	wt/bin/*-wt \
	wt/scripts/agents-smoke.sh

lint: lint-shell check-links

# Run bash -n and shellcheck (error severity only for now) on all shell scripts.
lint-shell:
	@for f in $(SHELL_SCRIPTS); do \
		echo "bash -n $$f"; \
		bash -n "$$f" || exit 1; \
	done
	@if command -v shellcheck > /dev/null 2>&1; then \
		shellcheck --severity=error $(SHELL_SCRIPTS); \
	else \
		echo "shellcheck not installed; skipping"; \
	fi

check-links:
	@bin/check-links

# Aggregate local verification across all monorepo components (mirrors CI).
test-all: lint
	cd modelman && uv sync && make check && make test
	cd wt && go build ./... && go vet ./... && go test ./...

install: ## Install all monorepo components (wt + modelman).
	cd wt && make install
	cd modelman && make install
