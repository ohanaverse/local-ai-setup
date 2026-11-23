.PHONY: help lint lint-shell lint-skills check-links test-all pre-pr install install-all install-skills test-install-skills update-tools install-skill-claude install-skill-pi install-skill-codex uninstall-skill-claude uninstall-skill-pi uninstall-skill-codex

.DEFAULT_GOAL := help

help: ## List the targets (the default)
	@awk -F':.*## ' '/^[a-zA-Z_-]+:.*## /{printf "  %-24s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

SHELL_SCRIPTS := \
	bin/lib/mlx-lm-resolve.sh \
	bin/lib/update-common.sh \
	bin/mlx-quantize \
	bin/check-config-dirs-untouched \
	bin/pre-pr \
	bin/test-install-skills \
	bin/update/* \
	bin/update-tools \
	benchmarks/qwen3.8-benchmark \
	benchmarks/qwen3.8-benchmark-multi \
	benchmarks/ornith-1.5-benchmark \
	benchmarks/ornith-1.5-benchmark-multi \
	benchmarks/mtplx-profile-matrix \
	benchmarks/lib/benchmark-common.sh \
	benchmarks/lib/benchmark-multi.sh \
	wt/bin/*-wt \
	wt/scripts/agents-smoke.sh \
	litellm-session-logs/02_export_proxy_server_request.sh \
	litellm-session-logs/03_capture_claude_code.sh

lint: lint-shell lint-skills check-links ## Run lint-shell, lint-skills and check-links

# Run bash -n and shellcheck (error severity only for now) on all shell scripts.
lint-shell: ## bash -n and shellcheck over the shell scripts
	@for f in $(SHELL_SCRIPTS); do \
		echo "bash -n $$f"; \
		bash -n "$$f" || exit 1; \
	done
	@if command -v shellcheck > /dev/null 2>&1; then \
		shellcheck --severity=error $(SHELL_SCRIPTS); \
	else \
		echo "shellcheck not installed; skipping"; \
	fi

# Every skill is named wt-<dir>: the directory name equals the frontmatter name, which starts with wt-.
lint-skills: ## Check every skill is named wt-<dir>
	@n=0; for f in .claude/skills/*/SKILL.md wt/.claude/skills/*/SKILL.md; do \
		dir=$$(basename "$$(dirname "$$f")"); \
		name=$$(sed -n 's/^name: *//p' "$$f" | head -1); \
		case "$$name" in wt-*) ;; *) echo "FAIL: $$f: name '$$name' lacks the wt- prefix"; exit 1;; esac; \
		[ "$$name" = "$$dir" ] || { echo "FAIL: $$f: name '$$name' differs from directory '$$dir'"; exit 1; }; \
		n=$$((n+1)); \
	done; echo "  OK: $$n skills named wt-<dir>"

check-links: ## Validate repo-relative links in tracked *.md
	@bin/check-links

# Aggregate local verification across all monorepo components (mirrors CI).
test-all: lint ## CI-equivalent: lint + llmbench + modeltiers + wt
	@$(MAKE) --no-print-directory test-install-skills
	cd llmbench && uv sync && make check && make test
	cd modeltiers && uv sync && make check && make test
	cd wt && go build ./... && go vet ./... && go test -timeout 30m -count=1 ./... && make single-p

test-install-skills: ## Check install-skills installs only for agents on PATH (scratch HOME)
	@bin/test-install-skills

pre-pr: ## Run before a PR: test-all in a clean scratch HOME + wt check + config-dir check (CI is off)
	@bin/pre-pr

install: ## Install all monorepo components (wt, wt-session and wt-model + llmbench + modeltiers)
	cd wt && make install
	cd llmbench && make install
	cd modeltiers && make install

update-tools: ## Update every tool the wt stack runs (see bin/update-tools)
	@bin/update-tools $(ARGS)

# wt-repo-cleanup is the one skill meant to run in any repo, so it is installed globally for
# each agent host. The other wt-* skills are repo-local. Each install replaces the old
# repo-cleanup copy (from the retired standalone repo) and any earlier wt-repo-cleanup.
SKILL_SRC := .claude/skills/wt-repo-cleanup/SKILL.md

install-skill-claude: ## Install wt-repo-cleanup for Claude Code (~/.claude/skills)
	@rm -rf $(HOME)/.claude/skills/repo-cleanup $(HOME)/.claude/skills/wt-repo-cleanup
	@mkdir -p $(HOME)/.claude/skills/wt-repo-cleanup
	@cp $(SKILL_SRC) $(HOME)/.claude/skills/wt-repo-cleanup/SKILL.md
	@echo "installed wt-repo-cleanup -> $(HOME)/.claude/skills/wt-repo-cleanup"

install-skill-pi: ## Install wt-repo-cleanup for Pi (~/.agents/skills)
	@rm -rf $(HOME)/.agents/skills/repo-cleanup $(HOME)/.agents/skills/wt-repo-cleanup
	@mkdir -p $(HOME)/.agents/skills/wt-repo-cleanup
	@cp $(SKILL_SRC) $(HOME)/.agents/skills/wt-repo-cleanup/SKILL.md
	@echo "installed wt-repo-cleanup -> $(HOME)/.agents/skills/wt-repo-cleanup"

install-skill-codex: ## Install wt-repo-cleanup for Codex (~/.codex/skills)
	@rm -rf $(HOME)/.codex/skills/repo-cleanup $(HOME)/.codex/skills/wt-repo-cleanup
	@mkdir -p $(HOME)/.codex/skills/wt-repo-cleanup
	@cp $(SKILL_SRC) $(HOME)/.codex/skills/wt-repo-cleanup/SKILL.md
	@echo "installed wt-repo-cleanup -> $(HOME)/.codex/skills/wt-repo-cleanup"

# One command for the whole machine: the binaries, then the skill for each agent whose command is
# on PATH (the test wt itself uses to launch an agent). An absent agent is skipped, not an error.
install-all: install ## Install everything: make install, then the wt-repo-cleanup skill for each of claude, pi, codex on PATH
	@$(MAKE) --no-print-directory install-skills
	@echo "next: this installs no model server. On a machine that serves models, run 'wt-model setup' (Setup tab: choose local, then a); a session machine uses 'wt config' (Mode tab)."

install-skills: ## Install wt-repo-cleanup for each of claude, pi and codex found on PATH
	@installed=""; skipped=""; \
	for a in claude pi codex; do \
		if command -v $$a > /dev/null 2>&1; then \
			$(MAKE) --no-print-directory install-skill-$$a || exit 1; \
			installed="$$installed $$a"; \
		else \
			echo "skipped $$a: not on PATH"; skipped="$$skipped $$a"; \
		fi; \
	done; \
	echo "skills: installed:$${installed:- none}; skipped:$${skipped:- none}"

uninstall-skill-claude: ## Remove wt-repo-cleanup from Claude Code
	@rm -rf $(HOME)/.claude/skills/wt-repo-cleanup

uninstall-skill-pi: ## Remove wt-repo-cleanup from Pi
	@rm -rf $(HOME)/.agents/skills/wt-repo-cleanup

uninstall-skill-codex: ## Remove wt-repo-cleanup from Codex
	@rm -rf $(HOME)/.codex/skills/wt-repo-cleanup

# Removal ignores PATH: an agent uninstalled since the install still has its copy to clean up.
uninstall-skills: uninstall-skill-claude uninstall-skill-pi uninstall-skill-codex ## Remove wt-repo-cleanup from claude, pi and codex, whether or not they are on PATH
	@echo "removed wt-repo-cleanup from claude, pi and codex"
