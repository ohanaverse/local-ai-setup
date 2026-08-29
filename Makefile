.PHONY: lint lint-shell check-links

SHELL_SCRIPTS := \
	bin/llm-isolate-provider \
	bin/llm-restore-providers \
	benchmarks/qwen3.8-benchmark \
	benchmarks/qwen3.8-benchmark-multi \
	benchmarks/ornith-1.5-benchmark \
	benchmarks/ornith-1.5-benchmark-multi

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
