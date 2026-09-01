# Verification notes

- Full test suite: pass (`go test ./... -count=1`, 13/13 packages)
- Static analysis: pass (`go vet ./...`)
- Build path: /Users/keith/.local/bin/wt
- `wt --help`: pass
- Pull request: https://github.com/ohanaverse/agent-worktree/pull/108
- Known caveats: OpenCode remains direct-to-Ollama in this release.
