# wt-model config home — Phase 4 stage 1 design

**Date:** 2026-11-17. **Scope:** the LiteLLM proxy pair (url + api key) moves to
`wt-model`'s own config home. This is Phase 4 stage 1 of the wt-model/wt-session
split (`2026-11-15-wt-model-session-split-design.md`, Migration), the change R4
deferred in words ("`wt-model`'s own config home waits for Phase 4's `wt-model
litellm set`", `2026-11-15-wt-session-client.md`:21) and the split spec's Phase 4
deferral named ("adds `wt-model litellm set` and its config home", spec:133).

Evidence base: the 2026-11-17 research fan-out (three reports with adversarial
verifications: the credential surface, the path/write/secret/CI/test surface,
the docs/spec/contract/style surface). Every file:line below was verified
there; corrected line numbers are the verifiers'.

## Goals

1. The proxy pair lives in `wt-model`'s own home: the url in
   `~/.config/wt-model/config.toml`, the api key in a 0600 file the config
   names. No key is ever stored in a TOML file again (new writes).
2. `wt-model litellm set|status` manage and show it. `wt litellm set|status`
   keep today's names, flags, output strings, JSON shape and exit codes, and
   work through the same reader and the same writer — one writer path, so the
   two binaries cannot diverge (wt/CLAUDE.md's rule for wt-owned files).
3. The legacy `[litellm]` table of `~/.config/agent-wt/config.toml` becomes a
   **read-only fallback**: consulted only when the new home has no `[litellm]`
   table, never written again, and it brings exactly one note line where the
   pair is the subject (a `status` run, a `wt-model serve` startup).

## Anti-goals

- No session-behavior change. Session code still never reads the pair (R4's
  boundary test stays green); the model-server token and `[model-server]` are
  untouched.
- No reload of a running `wt-model serve`. The upstream pair stays read once at
  startup (`modelserver.Options.Upstream/UpstreamKey`,
  `wt/internal/modelserver/server.go:44-45`); `wt-model-server.md` gains the
  rule ("a running server keeps the pair it started with; restart it after
  `wt-model litellm set`"). Per-request re-read is deferred, not designed here.
- No new env override. The home follows `baseConfigHome()` only
  (`wt/internal/config/config.go:753-767`), exactly like `agent-wt/`.
- No command moves. `sync|list|providers|restart` and the Models tab are Phase 4
  stage 2+, with the hard-cut ruling (old names print a pointer and exit 1).

## Non-goals, named so they stay untouched

- **pi's seeded `~/.pi/agent/models.json` and llmbench's `litellm_credentials()`**
  (`llmbench/src/llmbench/benchmark/_routes.py:57-74`). The seeded
  `providers.litellm` block carries the **gateway** origin and the model-server
  **token** (`wt/internal/agents/pi.go:20-33`, `pi_models.go:95-104,173`), not
  the proxy key, so it follows the token home — which does not move. Verified
  out of scope; llmbench keeps working unchanged.
- **llmbench's pinned wt message leads** (`wt_bridge.py:113-126`) and its
  pinned refusal line quoting `[litellm] is no longer used by wt`
  (`test_wt_bridge.py:182`): none of those strings change in stage 1 (see
  Ruling 14).
- `litellm.DatabaseURL()` / `ProxyEnv` / `ErrRegistryRedirected`: config.yaml
  and DSN surfaces, a different home. Untouched.

## The home

Layout, resolved through `baseConfigHome()` (`$XDG_CONFIG_HOME`, `~`-expanded
via `config.ExpandHome`, else `$HOME/.config`; empty home → nothing resolves):

    ~/.config/wt-model/config.toml     # not secret: url + key file pointer
    ~/.config/wt-model/litellm.key     # the api key, mode 0600 (default name)

`config.toml` shape — pinned by the fixture
`docs/contracts/wt-model-config.sample.toml` (new), held by a Go test in the
same change (J3):

```toml
# wt-model's configuration. [litellm] is written only by `wt-model litellm set`
# and `wt litellm set`; the api key is never here — it lives in the 0600 file
# that key_file names.
[litellm]
url = "http://localhost:4000"
key_file = "litellm.key"
```

- `url` — the proxy base URL (same semantics as the legacy `url`).
- `key_file` — the key file's path; a relative name resolves beside
  `config.toml`; `~` is expanded; absent means `litellm.key`. Hand-edited (a
  credential helper's file can be named here); `set` never writes this key.

### The key file (the `internal/tokenfile` rules, per paths-report §3.6)

- **Read:** `tokenfile.Load`. Group/other bits are a hard error with the fix
  sentence (`… is accessible by group or other (mode %s); run 'chmod 600 %s'`).
  A missing or empty file reads as **no key** (the consumer's existing "not
  configured" path reports it); whitespace is trimmed.
- **Write:** `config.WriteFileAtomic(path, key+"\n", 0o600)` — umask-independent,
  temp file cleaned up. `set --api-key ""` writes a 0600 empty file: wt never
  deletes a key file (it may name a user-managed file).
- **Never printed.** `status` shows `api_key_set` (JSON) or `maskKey` (text:
  `(unset)`, `***`, `***`+last 4 — `wt/cmd/wt-session/profile_slots.go:21-33`).
- A symlinked `config.toml` or key file is written through (links survive), the
  `internal/litellm` Save precedent (`configfile.go:770-773`); a dangling link
  is an error, never silently replaced.

### The one writer

`config.SetProxy(url, key *string) error` — a package function (no `*Config`
needed: `wt-model litellm set` must work when the legacy `config.toml` does not
load). `nil` leaves a field unchanged (`cmd.Flags().Changed` decides, C4);
this is the only writer of the new home, used by both CLIs.

- The TOML file: `config.WithFileLock(<home>/config.toml.lock)` (the one flock
  helper), fresh re-read, mutate only `[litellm]`, atomic
  `config.WriteFileAtomic`, keeping the file's existing mode (0644 when new).
- The key file: written under the same lock, so a reader never sees a url and a
  key from different writes.
- **No-op writes skip** (the `UpdateRegistry` precedent,
  `registry_write.go:168-187`): a `set` that changes nothing writes nothing and
  the bytes — comments included — survive. A write that does change something
  re-encodes and drops comments (the registry's documented behavior), then
  re-prepends the fixed header comment block above.
- A parse error in the home's `config.toml` fails `set` (loud); `set` writes
  only the new home, so a broken legacy `config.toml` or registry never blocks
  it (see Ruling 8).

## Resolution (one merge, at load)

`config.ResolveProxy() (ProxyPair, error)` with

```go
type ProxySource int
const (
    ProxySourceNone   ProxySource = iota // nothing configured
    ProxySourceHome                      // the new home's [litellm] table
    ProxySourceLegacy                    // the legacy [litellm] table (fallback)
)
type ProxyPair struct {
    URL, APIKey string
    Source      ProxySource
}
```

Precedence — **sources never mix**:

1. The new home's `config.toml` has a `[litellm]` table — present, even empty
   (`TestLitellmOwnEmptyTableIsATableNotAbsence`'s rule) — then it is
   authoritative and complete: url from `url`, key from `key_file`'s content. A
   missing or empty key file is "no key", never a fall-through.
2. Else the legacy `[litellm]` table supplies what it has (read through
   `readConfigFile`, the shared decoder — no migrations, no writes). A parse
   error there is an error of the resolution (loud).
3. Else: unconfigured (`ProxySourceNone`).

The result lands in `cfg.litellm` where `finalizeCfg` (`config.go:850-852`) and
`LoadSession` (`wt/internal/config/modelserver.go:62-64`) already copy the
table, so `LitellmBaseURL()` (TrimRight "/" kept) and `LitellmAPIKey()` resolve
the new home at every existing consumer — the route sync's proxy-url arguments
(`internal/litellm/service.go:379,970`), `lifecycle.RestartProxyTo`
(`profile_routes.go:151-153`), `ProbeProxy` (`:223-225`),
`bounceProxyAsync` (`routes.go:635-637`), `wt litellm restart`
(`litellm.go:765-779`) — with **zero call-site changes**. `LitellmTable` (the
parsed legacy table) is untouched by the merge and keeps round-tripping every
`config.toml` save unchanged (`TestLegacyLitellmKeysSurviveASave`). The source
is kept on the config as `(*Config).ProxySource() ProxySource`.

### The note

When the fallback supplied the values and the pair is the subject of the
command — `wt-model litellm status`, `wt litellm status`, `wt-model serve`
startup — one line goes to stderr (never stdout, M1), once per process:

```
%s: note: config.toml: [litellm] is a read-only fallback — '%s litellm set' writes %s now
```

Both `%s` are `config.BinaryName` (so `wt: … 'wt litellm set' …` in one binary
and `wt-model: … 'wt-model litellm set' …` in the other — per-binary prefixes
are the N1-correct divergence), the third is `config.ProxyHomePath()`. Shape
mirrors the retired-key note (`wt: note: config.toml: [litellm] enabled is no
longer used and is ignored`, `wt/cmd/wt-session/app.go:54`); `note:` is the M4
severity word for "something went wrong but the command goes on". `set` prints
no note (after it runs the home is authoritative).

## Command surface

`wt-model litellm set [--url URL] [--api-key KEY]` and
`wt-model litellm status [--json]` — twins of the `wt` pair, following the
repo's twin pattern (`launchSlotProfileRemote` / `beginRemoteSlotLaunch` held
by `TestSlotFlowParity` against one shared golden): each binary keeps its own
command definition (its `Use`/`Short`/`Long`, whose help names its own
binary); the status document and the key mask are shared code —
`config.WriteProxyStatus` and `config.MaskKey`, the `config.ConfigError`
precedent for shared presentation — and the shared fixture
`docs/contracts/litellm-cli.sample.json` `status` member is the golden both
sides' contract tests assert against (its `_comment` names both readers). A
divergence fails one of the two tests.

- `set`: flags `--url` ("LiteLLM proxy base URL"), `--api-key` ("LiteLLM proxy
  API key"); `Changed` picks the fields (C4); neither → `nothing to set: pass
  --url and/or --api-key`; success prints `litellm: updated`; exit 0/1.
- `status`: JSON exactly `{"url": …, "api_key_set": …}` (the `status` fixture
  member at `docs/contracts/litellm-cli.sample.json:36`, unchanged); text
  `litellm:\n  url: %s\n  api_key: %s\n` with `(unset)` / `maskKey`. Both files'
  read errors are the command's error (exit 1, loud).
- Gating: `wt-model litellm set` has **no** config gate (it writes only the new
  home — the escape hatch when `config.toml` is broken). `status` fails on an
  unreadable file that the resolution needs: home parse error always; the
  legacy parse error only when rule 1 did not short-circuit. `wt litellm
  set|status` rehome onto the same two functions and the same behavior (their
  old `cfgGuard` gate goes with the old write target — its danger, a
  `Save`-over-broken-config, is gone).
- `Long` texts carry the C2 exit paragraph and the `--json` schema path.
- Exit codes: 0 done, 1 any failure (C9/C11 through `exitcode.Report`; no new
  codes).

Messages and help that name the setup command — the two `loadServeConfig`
refusals (`wt/cmd/wt-model/serve.go:38,42`), `serve`'s `Long` (`:56`) and
every other hit of `wt litellm set` in code or help — name
`wt-model litellm set --url URL --api-key KEY` (M6: the exact command,
upper-case placeholders). The style-guide rule texts that quote the old
example (S2 `:173`, S7 `:178`, M6 `:44`) change in the same PR
(style-guide.md:11's rule).

## Legacy `[litellm]`, and config.toml's mode

- Never written again: `(*Config).UpdateLitellm` is deleted (its role is
  `SetProxy` on the new home). The `wt config` editor and `lockedApply`
  migrations keep round-tripping the table untouched (`configeditor/save.go:13-28`,
  `TestSave_PreservesConcurrentLitellmChange`).
- `config.LitellmConfigured()` is deleted (no production caller; after the merge
  it would answer from the wrong source).
- `enabled` keeps its semantics and its one note (unchanged).
- **Mode hardening** (`writeConfigFile`, `config.go:1398-1401`): the file is
  treated as secret-bearing when `[litellm].api_key` **or `[model-server]`'s
  inline token** is non-empty (the token's own comment already calls the file
  "not private", `modelserver.go:12-15`), and a write **never loosens** an
  existing stricter mode (the `internal/litellm` Save precedent,
  `configfile.go:774-778`). This is the research's permission-hole fix for the
  transition window in which the fallback key still lives in the file.

## Tests and isolation

- Every new test runs under `config.IsolateConfigHomeForTest`
  (`wt/internal/config/fortest.go:33-46`): the home resolves through
  `baseConfigHome()`, so the one XDG redirect covers it — no new env var to
  neutralize in `testmain_test.go`, llmbench's `_no_real_config`, or modeltiers'
  `_hermetic`. A guard test asserts `ProxyHomePath()` lands under the scratch
  home and never under the developer's.
- `bin/check-config-dirs-untouched`: `wt-model` joins `agent-wt local-ai
  litellm` on the names line (line 27) and the header's prose.
- Fixtures changed with the code (J3): new `docs/contracts/wt-model-config.sample.toml`;
  `docs/contracts/litellm-cli.sample.json` `_comment` gains the second reader.
- The `lost-update` tests that used `UpdateLitellm` as their vehicle
  (`lock_test.go:81,128`; `litellm_state_test.go:218`) retarget to the
  surviving writers: `SetProxy`×`SetProxy` on the new home (its own lock) and
  `configeditor`×`lockedApply` on `config.toml`.

## Rulings (decision — why — cost if wrong)

1. **Merge at load, not a resolver at ~9 call sites.** `finalizeCfg`/`LoadSession`
   already build `cfg.litellm`; one merge point cannot be forgotten by a future
   reader. Cost if wrong: a caller that wants raw table values must read
   `LitellmTable` explicitly (already true today).
2. **No new package — `internal/config/proxyhome.go`.** The merge, the shared
   decoder, `WriteFileAtomic` and the flock helper all live in `internal/config`;
   a sibling package would import-cycle against `WriteFileAtomic`, and moving
   that primitive is a separate refactor. Cost if wrong: `internal/config` grows
   ~300 lines; extracting a leaf later is mechanical.
3. **`key_file` pointer with a `litellm.key` default** — the `token_file`
   precedent (`config.ModelServerState`) and a real use (a credential helper's
   file). Cost if wrong: one extra key to document.
4. **Table presence is authority; sources never mix.** An empty `[litellm]`
   table means "configured, unconfigured pair", not "fall through". Cost if
   wrong: a user with a stray empty table sees `url: (unset)` instead of their
   legacy values — the note and `status` make it findable.
5. **No env override.** `IsolateConfigHomeForTest` covers every suite for free
   (paths report §5.4). Cost if wrong: an out-of-tree scratch run redirects
   `XDG_CONFIG_HOME`, which is already the documented lever.
6. **`status --json` stays exactly `{url, api_key_set}`.** Scripts pin it
   (fixture `_comment`); the note carries the source story. Cost if wrong: a
   future `source` field needs the fixture + both tests in one change (J3).
7. **The note fires at `status` and `serve` startup only**, once per process,
   per-binary prefixed. Cost if wrong: a noisy command would drop the note or
   move it behind `--json` silence (it already is: stderr).
8. **`set` is gateless; `status` is loud.** `set` writes only the new home, so
   the cfgGuard's protection (a `Save` over broken config) no longer applies;
   refusing would block the one command that can fix the pair. `status` keeps
   failing on an unreadable file it needs (the old `TestLitellmStateCommandsRefuseOnConfigError`
   property survives in retargeted form). Cost if wrong: `set` succeeding where
   it used to refuse is a behavior change for a failure case no script should
   rely on.
9. **Key file: loose = hard error; missing/empty = no key; never deleted.**
   Matches `tokenfile` where it must, and treats the clear operation
   (`--api-key ""`) without deleting a possibly user-managed file. Cost if
   wrong: a truncated key file reads "no key" until `set` rewrites it — the
   consumer refuses with the standard "not configured" message.
10. **No-op writes skip; a real write drops comments and re-prepends the fixed
    header.** The `UpdateRegistry` contract, byte for byte. Cost if wrong:
    mid-file hand comments die on the first real write (documented, as in
    `registry.toml`).
11. **Symlinked files are written through** (`EvalSymlinks` first), the
    `internal/litellm` precedent; dangling links error. Cost if wrong: none
    observed; the alternative (replacing the link) silently breaks dotfile
    repos.
12. **All setup-command messages name `wt-model litellm set`** — the durable
    home of the command (stage 2+ hard-cuts the `wt` name). Cost if wrong: the
    message names a command whose binary is not installed yet on a mixed
    machine — both names work in stage 1.
13. **Read-once upstream key kept and documented.** Cost if wrong: a `set`
    against a running server needs a restart (said in `wt-model-server.md` and
    the deferred list).
14. **The `[litellm] is no longer used by wt` refusal clause is unchanged.**
    llmbench pins it (`test_wt_bridge.py:182`); it is still true for sessions.
    Cost if wrong: the clause reads slightly broadly for host commands; the
    deferred list owns the wording fix + pin update.
15. **The dated `2026-11-15-wt-session-client.md` plan stays historical** (its
    R4/R17 text records the then-plan); the split spec is the living authority
    and gets amended (`:75`, `:77`, `:133`). Cost if wrong: one stale sentence
    in a dated doc, pointed at by spec:133.

## Deferred / follow-ups (for the PR body)

- Reload rule for a running server's cached upstream key (per request vs. on
  `set`; needs its own small design).
- `--api-key` on argv is `ps`-visible (pre-existing); a
  read-key-from-stdin/file flag could follow the `tokenCreateCmd` recipe.
- Stage 2+ command moves with the hard cut (2026-11-17 ruling).
- The `[litellm] is no longer used by wt` clause wording (+ the llmbench pin).
- Extracting `proxyhome.go` into a leaf package once `WriteFileAtomic` moves.
