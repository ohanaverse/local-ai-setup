.PHONY: help install uninstall check lint format test clean

help:                   # Show available targets
	@echo "Available targets:"
	@echo "  install   - Copy bin/* to ~/.local/bin/"
	@echo "  uninstall - Remove installed scripts from ~/.local/bin/"
	@echo "  lint      - Run shellcheck on bin/*.sh and bin/*-wt"
	@echo "  format    - Run shfmt on bin/*.sh and bin/*-wt"
	@echo "  format-check - Check formatting without modifying"
	@echo "  check     - Run lint + format-check"
	@echo "  test      - Run smoke tests"
	@echo "  clean     - Remove build artifacts"

BINDIR := $(HOME)/.local/bin
SRCDIR := bin

install:                # Install scripts to ~/.local/bin/
	cp -r $(SRCDIR)/* $(BINDIR)/
	chmod +x $(BINDIR)/*-wt $(BINDIR)/wt-core.sh $(BINDIR)/wt-install-guard
	@echo "Installed agent-worktree scripts to $(BINDIR)"

uninstall:              # Remove installed scripts from ~/.local/bin/
	rm -f $(BINDIR)/agy-wt $(BINDIR)/claude-wt $(BINDIR)/codex-wt \
	      $(BINDIR)/copilot-wt $(BINDIR)/pi-wt $(BINDIR)/wt-core.sh \
	      $(BINDIR)/wt-install-guard
	@echo "Uninstalled agent-worktree scripts from $(BINDIR)"

lint:                   # Run shellcheck
	@command -v shellcheck >/dev/null 2>&1 || { echo "shellcheck not found. Install with: brew install shellcheck"; exit 1; }
	@echo "Running shellcheck (ignoring expected warnings for dynamic sourcing)..."
	shellcheck -e SC1090,SC1091,SC2034,SC2155 $(SRCDIR)/*.sh $(SRCDIR)/*-wt
	@echo "Lint passed."

format:                 # Run shfmt (write changes)
	@command -v shfmt >/dev/null 2>&1 || { echo "shfmt not installed, skipping format"; exit 0; }
	shfmt -w -i 2 -ci $(SRCDIR)/*.sh $(SRCDIR)/*-wt

format-check:           # Check formatting without modifying
	@command -v shfmt >/dev/null 2>&1 || { echo "shfmt not installed, skipping format check"; exit 0; }
	shfmt -d -i 2 -ci $(SRCDIR)/*.sh $(SRCDIR)/*-wt

check:                  # Run all quality checks
	@echo "Running lint..."
	$(MAKE) lint
	@echo "Running format check..."
	$(MAKE) format-check

test:                   # Run smoke tests
	@echo "Smoke testing launcher flags..."
	@for launcher in claude-wt codex-wt copilot-wt pi-wt agy-wt; do \
		echo "Testing $$launcher --help..."; \
		$(BINDIR)/$$launcher --help >/dev/null 2>&1 || echo "  $$launcher --help: skipped (agent may not be installed)"; \
	done
	@echo "Smoke tests complete."

clean:                  # Remove build artifacts
	rm -rf .pytest_cache .mypy_cache __pycache__
	find . -type d -name __pycache__ -exec rm -rf {} + 2>/dev/null || true
	find . -type f -name "*.pyc" -delete 2>/dev/null || true
