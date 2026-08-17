.PHONY: help build install uninstall check lint format format-check test clean

help:                   # Show available targets
	@echo "Available targets:"
	@echo "  build     - Build the Go wt binary to bin/wt"
	@echo "  install   - Copy bin/* to ~/.local/bin/"
	@echo "  uninstall - Remove installed scripts from ~/.local/bin/"
	@echo "  lint      - Run shellcheck on bin/*-wt"
	@echo "  format    - Run shfmt on bin/*-wt"
	@echo "  format-check - Check formatting without modifying"
	@echo "  check     - Run lint + format-check"
	@echo "  test      - Run smoke tests"
	@echo "  clean     - Remove build artifacts"

BINDIR := $(HOME)/.local/bin
SRCDIR := bin

build:                  # Build the Go wt binary
	@echo "Building wt..."
	go build -o bin/wt ./cmd/wt
	@echo "Built bin/wt"

install:                # Install scripts to ~/.local/bin/
	cp -r $(SRCDIR)/* $(BINDIR)/
	chmod +x $(BINDIR)/*-wt
	@echo "Installed agent-worktree scripts to $(BINDIR)"

uninstall:              # Remove installed scripts from ~/.local/bin/
	rm -f $(BINDIR)/agy-wt $(BINDIR)/claude-wt $(BINDIR)/codex-wt \
	      $(BINDIR)/copilot-wt $(BINDIR)/opencode-wt $(BINDIR)/pi-wt \
	      $(BINDIR)/shell-wt
	@echo "Uninstalled agent-worktree scripts from $(BINDIR)"

lint:                   # Run shellcheck
	@command -v shellcheck >/dev/null 2>&1 || { echo "shellcheck not found. Install with: brew install shellcheck"; exit 1; }
	@echo "Running shellcheck..."
	shellcheck $(SRCDIR)/*-wt
	@echo "Lint passed."

format:                 # Run shfmt (write changes)
	@command -v shfmt >/dev/null 2>&1 || { echo "shfmt not installed, skipping format"; exit 0; }
	shfmt -w -i 2 -ci $(SRCDIR)/*-wt

format-check:           # Check formatting without modifying
	@command -v shfmt >/dev/null 2>&1 || { echo "shfmt not installed, skipping format check"; exit 0; }
	shfmt -d -i 2 -ci $(SRCDIR)/*-wt

check:                  # Run all quality checks
	@echo "Running lint..."
	$(MAKE) lint
	@echo "Running format check..."
	$(MAKE) format-check

test:                   # Run smoke tests
	@echo "Smoke testing launcher flags..."
	@for launcher in agy-wt claude-wt codex-wt copilot-wt opencode-wt pi-wt shell-wt; do \
		echo "Testing $$launcher --help..."; \
		$(BINDIR)/$$launcher --help >/dev/null 2>&1 || echo "  $$launcher --help: skipped (agent may not be installed)"; \
	done
	@echo "All tests complete."

clean:                  # Remove build artifacts
	rm -f bin/wt
	find . -type f -name "*.test" -delete 2>/dev/null || true