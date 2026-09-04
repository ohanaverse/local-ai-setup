# shell-wt without a modelman registry

## Summary

`shell-wt` (and any command agent) should work on a fresh machine where the
modelman registry has not been set up yet. Command agents have no model layer,
so a missing `registry.toml` must not block them. Real (model-driven) agents
keep the existing fail-closed behavior and its clear "seed with `modelman
migrate`" message.

## Motivation

`shell-wt` only offers worktree/branch selection and runs a shell command — it
never needs models. But today it fails on a machine with no modelman config:

1. `newApp()` calls `config.Load()`, which calls `loadRegistry()`.
2. `loadRegistry()` fails closed when `registry.toml` is missing, returning
   `"model registry not found at ... — seed it with modelman migrate"`.
3. That error lands in `a.cfgErr`, and `rootCmd().RunE` bails at the
   `a.cfgErr` gate (`main.go:279`) — before it ever checks whether the agent
   is a command.

So even though `shell` has no model layer, a missing registry blocks it.

## Design

### 1. Sentinel error in `internal/config/registry.go`

Add a package-level sentinel and wrap it in `loadRegistry()`'s
`os.IsNotExist` branch so the user-facing message is preserved:

```go
var ErrRegistryMissing = errors.New("model registry not found")

// in loadRegistry, os.IsNotExist branch:
return nil, nil, fmt.Errorf("%w at %s — seed it with `modelman migrate`", ErrRegistryMissing, path)
```

`errors.Is(err, config.ErrRegistryMissing)` works; the message is unchanged.

### 2. Conditional gate in `cmd/wt/main.go` (line 279)

Relax the `a.cfgErr` check only for pinned command agents whose sole problem
is a missing registry:

```go
if a.cfgErr != nil && !(agent != "" && agents.IsCommand(agent) && errors.Is(a.cfgErr, config.ErrRegistryMissing)) {
    return fmt.Errorf("config error: %w (run `wt config` to repair)", a.cfgErr)
}
```

`agent` is already in scope at that point (from `--agent`). The check is
general — it covers `shell` and any future command agent.

## Edge cases (all preserved as-is)

- **Malformed `config.toml`** — still fails for shell (genuine problem, not a
  fresh-machine case).
- **Malformed `registry.toml`** — still fails for shell (file exists but is
  broken).
- **Real agents (`claude`, etc.)** — still fail with the clear "seed with
  `modelman migrate`" message.
- **`wt config`, `--init`, `--version`, `--check-guard`, `--no-guard`,
  `--debug-*`** — unaffected (handled before the gate).
- **Unpinned `wt` (TUI)** — still fails, since it needs models.

## Testing

- `loadRegistry` returns `ErrRegistryMissing` when the file is absent.
- A command agent (`shell`) launches successfully with a missing registry
  (both non-TUI and TUI paths).
- A non-command agent still fails with a missing registry.

## Open Questions

None.

## Trade-offs

**Chosen: tolerate only the *missing* registry for command agents.** A
malformed registry still fails for shell. This keeps the change minimal and
surfaces genuine config problems, while covering the fresh-machine scenario.
