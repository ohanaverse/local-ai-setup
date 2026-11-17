# wt-model config home Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** the LiteLLM proxy pair (url + api key) moves to `~/.config/wt-model/`
with the key in a 0600 file, managed by new `wt-model litellm set|status` and
by today's `wt litellm set|status` through one shared writer, with the legacy
`[litellm]` table a read-only fallback.

**Architecture:** one merge in `internal/config` (new `proxyhome.go`) resolves
the new home first and the legacy `[litellm]` table second into `cfg.litellm`,
so every existing consumer keeps working untouched; `config.SetProxy` is the
one writer of the new home; `cmd/wt-model/litellm.go` twins
`cmd/wt-session/litellm.go`'s `set|status`, both contract-tested against one
fixture member.

**Tech Stack:** Go 1.26.7 (module `wt/`), TOML (`toml` package already in use),
cobra commands, stdlib-only tests.

**Spec:** `docs/superpowers/specs/2026-11-17-wt-model-config-home-design.md`
(this plan argues from it; read both).

## Global Constraints

- Commit messages end with `Co-Authored-By: Claude Code <noreply@anthropic.com>`.
- Every stderr line starts with the binary name (`wt:` / `wt-model:`) — style
  guide N1; a note carries the `note:` severity word (M4); errors are one line
  through `exitcode.Report` (M3, C11); these commands exit 0 (done) or 1
  (failure) only (C9).
- The api key is never printed: JSON shows `api_key_set`, text shows `maskKey`
  (`(unset)`, `***` for ≤4 chars, `***`+last 4 otherwise).
- `wt litellm set|status` keep today's flags and exact output strings:
  `litellm: updated`; `litellm:\n  url: %s\n  api_key: %s\n` (url prints
  `(unset)` when empty); error `nothing to set: pass --url and/or --api-key`;
  JSON exactly `{"url": …, "api_key_set": …}`.
- Every shape another program reads is pinned by a `docs/contracts/` fixture
  and a Go test, both changed in one commit (style guide J3).
- Secrets never live in a non-private TOML; the key file is 0600 — group/other
  bits refuse with `… run 'chmod 600 <path>'`; a missing or empty key file is
  "no key"; wt never deletes a key file.
- Every read-modify-write flocks via `config.WithFileLock`; writes are atomic
  via `config.WriteFileAtomic`.
- Messages naming the setup command say exactly
  `wt-model litellm set --url URL --api-key KEY` (M6 placeholders).
- Tests never touch the real config home: everything runs under
  `config.IsolateConfigHomeForTest`; the new home resolves through
  `baseConfigHome()` and adds **no** env override.
- The key-file read goes through `internal/tokenfile` (`tokenfile.Load`), never
  a bespoke read.

## Review Focus

- Two `set` runs racing (two processes) must leave a whole pair, never a url
  from one write and a key from the other — one flock around both files.
  Pinned: `TestSetProxyConcurrentSetsLoseNoField` (Task 1).
- A half-configured pair (url only; `key_file` missing; empty key file) must
  read as "not configured" and never mix with legacy values. Pinned:
  `TestResolveProxyNeverMixesSources` (Task 1).
- A group-readable key file must refuse with the `chmod 600` sentence in both
  CLIs and at `wt-model serve` startup, and never proceed. Pinned:
  `TestResolveProxyRefusesALooseKeyFile` (Task 1) and
  `TestServeRefusesALooseKeyFile` (Task 3).
- A redirected `XDG_CONFIG_HOME` must resolve both the new home and the legacy
  fallback under it (a test or scratch run never touches the developer's home).
  Pinned: `TestProxyHomeFollowsTheConfigHome` (Task 4), plus `wt-model` in
  `bin/check-config-dirs-untouched`.
- An existing install (legacy `[litellm]` only) must keep working with exactly
  one note line at `status`/`serve`, and every `config.toml` save must
  round-trip the table and keep a tightened mode. Pinned:
  `TestLitellmStatusNotesTheLegacyFallback` (Task 2),
  `TestServeNotesTheLegacyFallback` (Task 3) and the Task 4 mode tests.

---

### Task 1: `internal/config/proxyhome.go` — the home, the key file, the merge

**Files:**
- Create: `wt/internal/config/proxyhome.go`, `wt/internal/config/proxyhome_test.go`, `docs/contracts/wt-model-config.sample.toml`
- Modify: `wt/internal/config/config.go` (`finalizeCfg` copy :850-852, the `ProxySource` accessor), `wt/internal/config/modelserver.go` (`LoadSession` copy :62-64)
- Test: `wt/internal/config/proxyhome_test.go` (new)

**Interfaces:**
- Consumes: `config.baseConfigHome()` (config.go:753-767), `config.readConfigFile` (config.go:1412-1425), `config.WithFileLock`, `config.WriteFileAtomic`, `config.ExpandHome`, `tokenfile.Load` (`wt/internal/tokenfile/tokenfile.go:55-72`), `Config.LitellmTable` (config.go:727), `Config.litellm` (config.go:728).
- Produces (Tasks 2–3 rely on these exact names):
  - `type ProxySource int` with `ProxySourceNone`, `ProxySourceHome`, `ProxySourceLegacy`
  - `type ProxyPair struct { URL, APIKey string; Source ProxySource }`
  - `func ProxyHomePath() string` — `<baseConfigHome>/wt-model/config.toml` ("" with no home)
  - `func ResolveProxy() (ProxyPair, error)` — precedence home → legacy → none, sources never mixed
  - `func SetProxy(url, key *string) error` — the one writer; nil leaves that field unchanged
  - `func ProxyFallbackNote() string` — the one note line (no trailing newline), `%s: note: config.toml: [litellm] is a read-only fallback — '%s litellm set' writes %s now` with `config.BinaryName` twice then `ProxyHomePath()`
  - `func (c *Config) ProxySource() ProxySource` — where `cfg.litellm`'s pair came from
  - `func MaskKey(key string) string` — `""` → `(unset)`, ≤4 chars → `***`, else `***`+last 4 (the logic `cmd/wt-session/profile_slots.go:21-33` holds today; both binaries print through it)
  - `func WriteProxyStatus(w io.Writer, pair ProxyPair, asJSON bool) error` — the `status` document: text `litellm:\n  url: %s\n  api_key: %s\n` (the url prints `(unset)` when empty) or JSON `{"url": …, "api_key_set": …}`; its golden is `docs/contracts/litellm-cli.sample.json`'s existing `status` member

- [ ] **Step 1: write the fixture and its failing holding test**

`docs/contracts/wt-model-config.sample.toml` holds exactly the spec's sample
(header comment + `[litellm]` with `url = "http://localhost:4000"`,
`key_file = "litellm.key"`). `TestProxyHomeFixtureMatchesTheTypes` (in
`proxyhome_test.go`) parses it into the same struct `ResolveProxy` uses and
fails until the type and the fixture agree (the `modelapi.TestFixtureMatchesTypes`
pattern).

Run: `go -C wt test ./internal/config -run TestProxyHomeFixture -count=1`
Expected: FAIL (no such type/parse)

- [ ] **Step 2: write the failing resolution tests**

In `proxyhome_test.go`, each under `config.IsolateConfigHomeForTest` semantics
(the package `TestMain` already isolates):

- `TestResolveProxyPrefersTheHomeTable` — home `[litellm]` with url+key_file
  whose file holds `sk-test` resolves `{URL: "http://localhost:4001",
  APIKey: "sk-test", Source: ProxySourceHome}` even when the legacy table holds
  different values.
- `TestResolveProxyNeverMixesSources` — home table with url but a missing
  `key_file` resolves `APIKey: ""` (never the legacy key) and the legacy-only
  case resolves the legacy pair with `ProxySourceLegacy`; no table anywhere →
  `ProxySourceNone`. An empty `[litellm]` table is authoritative (empty pair),
  not absence.
- `TestResolveProxyRefusesALooseKeyFile` — a 0644 key file is an error
  containing `chmod 600`; an empty key file is `APIKey: ""`, not an error; the
  key is whitespace-trimmed.
- `TestResolveProxyLegacyNeedsNoMigrations` — a legacy-only `config.toml`
  resolves without rewriting that file (bytes identical after).
- `TestMaskKey` — `""` → `(unset)`, `ab` → `***`, `sk-12345678` → `***5678`.
- `TestWriteProxyStatusMatchesTheFixtureMember` — the text block byte-for-byte
  (url `(unset)` when empty, `maskKey` for the key) and the JSON document equal
  to `docs/contracts/litellm-cli.sample.json`'s `status` member (that fixture
  already exists; Task 2 only names this test file in its `_comment`).

Run: `go -C wt test ./internal/config -run TestResolveProxy -count=1`
Expected: FAIL (undefined: ResolveProxy)

- [ ] **Step 3: implement the home paths, key-file rules and `ResolveProxy` in `proxyhome.go`**

The home is `filepath.Join(baseConfigHome(), "wt-model")`; `key_file` (default
`litellm.key`) is `ExpandHome`d and joined to the home dir when relative. The
legacy read is `readConfigFile` on `Path()` — no `Load`, no migrations. The
key read is `tokenfile.Load` mapped as the spec rules: `KindLoose` wraps to an
error keeping the `chmod 600` sentence; `KindStat`/`KindEmpty` are "no key";
`KindRead` is the error. `MaskKey` and `WriteProxyStatus` go in the same
file — the shared presentation both binaries print (the `config.ConfigError`
precedent for shared presentation in `internal/config`); `WriteProxyStatus`
prints the url's `(unset)` itself and masks the key with `MaskKey`.

- [ ] **Step 4: run the tests to verify they pass**

Run: `go -C wt test ./internal/config -run 'TestProxyHome|TestResolveProxy' -count=1`
Expected: PASS

- [ ] **Step 5: write the failing writer tests**

- `TestSetProxyWritesOnlyTheNewHome` — `SetProxy(ptr("http://localhost:4002"),
  ptr("sk-new"))` creates the home's `config.toml` (0644, header comment,
  `[litellm] url`/`key_file = "litellm.key"`) and `litellm.key` (0600, content
  `sk-new`); a pre-existing legacy `config.toml` is byte-identical after.
- `TestSetProxyClearsTheKeyWithoutDeletingTheFile` — `SetProxy(nil, ptr(""))`
  writes a 0600 empty key file and leaves `url` unchanged.
- `TestSetProxyNoOpPreservesBytes` — a `SetProxy` that changes nothing leaves
  both files byte-identical (hand comments included); a changing write
  re-prepends the header comment.
- `TestSetProxyConcurrentSetsLoseNoField` — two goroutines set disjoint fields;
  the final pair is one writer's whole pair or the merged intent, never a torn
  url/key combination (the flock + fresh re-read).
- `TestSetProxyWritesThroughSymlinks` — symlinked `config.toml` and key file
  survive as links with updated targets; a dangling link is an error.
- `TestSetProxyRefusesAnUnparseableHome` — a broken home `config.toml` fails
  `SetProxy` and is left untouched.

Run: `go -C wt test ./internal/config -run TestSetProxy -count=1`
Expected: FAIL (undefined: SetProxy)

- [ ] **Step 6: implement `SetProxy` in `proxyhome.go`**

`config.WithFileLock(ProxyHomePath()+".lock")` around: `EvalSymlinks` both
paths, fresh parse of the home file (its `[litellm]` struct is the whole
document type), apply the non-nil fields, compare before/after (skip writes
when unchanged), `WriteFileAtomic` with the existing mode (0644 new) for the
TOML and 0600 for the key, header comment re-prepended on a real TOML write.

- [ ] **Step 7: run the writer tests, then the merge tests**

- [ ] **Step 8: write the failing merge tests and implement the merge**

- `TestLoadResolvesTheProxyPairIntoTheRuntimeState` — after `Load`, both
  `LitellmBaseURL()` (trailing-slash trim kept) and `LitellmAPIKey()` report the
  **home** pair when the home table exists, the legacy pair otherwise, and
  `ProxySource()` reports which; `LoadSession` behaves the same; `LitellmTable`
  still holds the legacy parse and `TestLegacyLitellmKeysSurviveASave` stays
  green.
- Implement one unexported `applyProxyState(*Config)` called from `finalizeCfg`
  and `LoadSession`'s copy site.
- The old writer's deletion (`UpdateLitellm`, `LitellmConfigured`) and every
  test that names them are Task 2's — Task 2 rehomes their one production
  caller. Do not touch them here; this task's commit must stay buildable and
  fully tested on its own.

Run: `go -C wt test ./internal/config -count=1`, then `go -C wt build ./...`
Expected: PASS, clean build

- [ ] **Step 9: commit**

```bash
git add wt/internal/config docs/contracts/wt-model-config.sample.toml
git commit -m "feat(config): the wt-model config home — resolution, key file, one writer"
```

---

### Task 2: the command surface — `wt-model litellm set|status`, rehomed `wt` pair

**Files:**
- Create: `wt/cmd/wt-model/litellm.go`, `wt/cmd/wt-model/litellm_test.go`
- Modify: `wt/cmd/wt-model/main.go` (register beside `root.AddCommand(serveCmd(mk))`, :34), `wt/cmd/wt-session/litellm.go` (`runLitellmSet` :605-618, `statusC`/`setC` RunE :661-687), `wt/cmd/wt-session/litellm_test.go` (:346-456, :498-541, :2051-2087), `wt/cmd/wt-session/profile_slots.go` (delete the local `maskKey` :21-33, rehome its call :194), `wt/internal/config/config.go` (delete `UpdateLitellm` :69-98 and `LitellmConfigured` :100-101), `wt/internal/config/litellm_state_test.go`, `wt/internal/config/lock_test.go`, `docs/contracts/litellm-cli.sample.json` (`_comment` :2)
- Test: `wt/cmd/wt-model/litellm_test.go` (new)

**Interfaces:**
- Consumes (Task 1): `config.ResolveProxy`, `config.SetProxy`, `config.ProxyFallbackNote`, `config.WriteProxyStatus`, `config.MaskKey`, `ProxyPair`/`ProxySource`.
- Produces: `wt-model litellm set|status` on the command tree (Task 3 rewords
  their `Long` help's setup mentions only if any remain); the `status` fixture
  member is now held by tests in **both** cmd packages.

- [ ] **Step 1: write the failing wt-model command tests**

In `wt/cmd/wt-model/litellm_test.go` (the package `TestMain` already isolates):

- `TestLitellmStatusMatchesTheFixture` — with a home pair set, `status --json`
  marshals to exactly the fixture's `status` member key set and values
  (`docs/contracts/litellm-cli.sample.json`, member `status` — the same
  assertion `TestLitellmStatusHasNoEnabledKey` makes on the `wt` side).
- `TestLitellmStatusTextMasksKey` — the text block, `(unset)` url, `maskKey`
  (`(unset)`/`***`/`***`+last 4), byte-for-byte the `wt` side's shape.
- `TestLitellmSetIndependentAndClear` — `Changed` semantics: one flag never
  clobbers the other; `--api-key ""` clears; success line `litellm: updated`;
  persistence visible to `ResolveProxy`.
- `TestLitellmSetNeedsSomething` — `nothing to set: pass --url and/or --api-key`.
- `TestLitellmStatusNotesTheLegacyFallback` — legacy-only values: `status`
  prints the pair and one `wt-model: note: …` line on **stderr**, none on
  stdout; `--json` stdout stays the single JSON document.

Run: `go -C wt test ./cmd/wt-model -run TestLitellm -count=1`
Expected: FAIL (no litellm command)

- [ ] **Step 2: implement `wt/cmd/wt-model/litellm.go`**

A `litellmCmd()` returning the `litellm` group with `status` and `set` twins of
the `wt` commands (command definitions per-binary; documents shared):
`status` prints `config.WriteProxyStatus`'s document for
`config.ResolveProxy()`'s pair, `set` calls `config.SetProxy` with the
`Changed` fields. `Use`/`Short`/`Args:
cobra.NoArgs`/`SilenceUsage` mirror the `wt` commands (C1); `Long` carries the
C2 exit paragraph (`0 done, 1 any failure`) and `--json` help is
`print one JSON document` plus `docs/contracts/litellm-cli.sample.json` (C3).
Register it in `main.go` beside `serveCmd`.

- [ ] **Step 3: run the wt-model tests to verify they pass**

Run: `go -C wt test ./cmd/wt-model -count=1`
Expected: PASS

- [ ] **Step 4: write the failing wt-side changes, then rehome them**

- `runLitellmSet` drops its `cfg` parameter and calls `config.SetProxy(url,
  key)`; `setC`'s RunE drops the `cfgGuard` wrapper (the design's Ruling 8);
  `statusC`'s RunE prints `config.WriteProxyStatus`'s document for
  `config.ResolveProxy()`'s pair, and `config.ProxyFallbackNote()` to stderr
  when `Source == ProxySourceLegacy`; `cmd/wt-session`'s `maskKey`
  (`profile_slots.go:21-33`) is deleted and its call (`:194`) rehomed to
  `config.MaskKey`.
- Rewrite `TestLitellmStateCommandsRefuseOnConfigError` as
  `TestLitellmStateCommandsFailOnAnUnreadableProxyConfig`: `status` exits 1 on
  an unparseable home file, and on an unparseable legacy file only when the
  home has no `[litellm]` table; `set` succeeds against a broken legacy file
  (writes the home) and fails on a broken home file.
- `TestLitellmSetIndependentAndClear` (:419) now asserts the home's files (url
  in `config.toml`, key 0600 in `litellm.key`) and that the legacy table is
  untouched.
- `TestLitellmStatusAndSet` / `TestLitellmStatusTextMasksKey` /
  `TestLitellmStatusHasNoEnabledKey` keep their assertions (shapes unchanged).
- Delete `(*Config).UpdateLitellm` and `(*Config).LitellmConfigured` (their one
  production caller is `runLitellmSet`, rehomed above). Drop
  `TestUpdateLitellmPersists` and `TestUpdateLitellmRollsBackOnSaveFailure`
  (Task 1's `TestSetProxyWritesOnlyTheNewHome` /
  `TestSetProxyRefusesAnUnparseableHome` cover those behaviors on the new
  home); retarget `lock_test.go`'s `TestUpdateLitellmPreservesConcurrentAgentChange`
  → a `configeditor`-vs-`lockedApply` concurrent pair, and
  `TestPatchSavePreservesConcurrentLitellmChange` /
  `TestLockedApplyPreservesConcurrentLitellmChange` → concurrent saves of
  `Agents` and `DefaultTag` through the same vehicles (the issue-#143 lost-update
  race disappears: `set` no longer touches `config.toml`).
- `docs/contracts/litellm-cli.sample.json` `_comment` reads "Read by
  wt/cmd/wt-session/litellm_test.go and wt/cmd/wt-model/litellm_test.go".

Run: `go -C wt test ./cmd/wt-session -run 'TestLitellm' -count=1`
Expected: PASS

- [ ] **Step 5: verify the twins cannot diverge**

Both packages' fixture tests green in one run:
`go -C wt test ./cmd/wt-session ./cmd/wt-model -run 'TestLitellmStatus' -count=1`

- [ ] **Step 6: commit**

```bash
git add wt/cmd docs/contracts/litellm-cli.sample.json
git commit -m "feat(wt-model): litellm set|status, and the wt pair through the new home"
```

---

### Task 3: serve wiring, messages, the note at startup

**Files:**
- Modify: `wt/cmd/wt-model/serve.go` (`loadServeConfig` :29-45, `Long` :54-70), `wt/internal/modelserver/deps.go` (the `wt litellm set` wording near :169-171), every remaining `wt litellm set` hit under `wt/` (grep sweep)
- Test: `wt/cmd/wt-model/serve_test.go` (:138-144 and neighbors)

**Interfaces:**
- Consumes (Task 1): `config.ResolveProxy`, `config.ProxyFallbackNote`.
- Produces: refusals and help naming `wt-model litellm set --url URL --api-key KEY`;
  `wt-model serve` prints the fallback note once on stderr before listening
  (only when `Source == ProxySourceLegacy`).

- [ ] **Step 1: write the failing tests**

- `TestServeRefusesUnconfiguredLiteLLM` asserts the error contains
  `wt-model litellm set` (today it asserts `wt litellm set`).
- `TestServeRefusesALooseKeyFile` (Review Focus): a 0644 key file fails startup
  with the `chmod 600` sentence.
- `TestServeNotesTheLegacyFallback`: a legacy-only pair starts (with a fake
  deps factory) and the stderr carries exactly one `wt-model: note: …` line
  before the announce line; a home pair produces none.

Run: `go -C wt test ./cmd/wt-model -run TestServe -count=1`
Expected: FAIL on the new assertions

- [ ] **Step 2: reword and wire**

`loadServeConfig` keeps `config.Load()` + `cfg.LitellmBaseURL()/LitellmAPIKey()`
(Task 1's merge makes that the new home) and its two refusals name
`wt-model litellm set --url URL --api-key KEY`; `serve`'s `Long` (the
`configured by 'wt litellm set'` sentence) follows. The note is printed in
`serveCmd`'s RunE after a successful resolve, before `NewServer`. Grep sweep:
`grep -rn "wt litellm set" wt/ --include='*.go'` — Go help text, comments,
`deps.go` wording and test expectations name `wt-model litellm set`, except a
test's literal quoting of old output it deliberately pins. Every `.md` hit is
Task 5's (including `wt/docs/style-guide.md`'s S2/S7/M6 examples).

- [ ] **Step 3: run the scoped suites**

Run: `go -C wt test ./cmd/wt-model ./internal/modelserver -count=1` and
`go -C wt vet ./...`
Expected: PASS, clean vet

- [ ] **Step 4: commit**

```bash
git add wt
git commit -m "feat(wt-model): serve reads the config home; setup messages name wt-model litellm set"
```

---

### Task 4: mode hardening, the CI guard, isolation assertions

**Files:**
- Modify: `wt/internal/config/config.go` (`writeConfigFile` mode block :1398-1401), `bin/check-config-dirs-untouched` (names line 27 + header prose)
- Test: `wt/internal/config/litellm_state_test.go` (mode cases) and `wt/internal/config/proxyhome_test.go` (isolation guard)

**Interfaces:**
- Consumes (Task 1): `ProxyHomePath` (for the isolation assertion). The mode
  rule reads the pre-existing `Config.LitellmTable.APIKey` and the
  `[model-server]` token field (`ModelServerState.Token`), not Task 1's API.
- Produces: `writeConfigFile` treats `[model-server]`'s inline token as
  secret-bearing and never loosens an existing stricter mode; `bin/check-config-dirs-untouched`
  fails on a real `$HOME/.config/wt-model` or `$XDG_CONFIG_HOME/wt-model`.

- [ ] **Step 1: write the failing mode tests**

- `TestWriteConfigFileTreatsAnInlineTokenAsSecret` — a saved config with
  `[model-server] token = "…"` (no `[litellm]` key) writes 0600.
- `TestWriteConfigFileNeverLoosens` — a 0600 `config.toml` whose key is later
  cleared stays 0600; a 0644 file with a key becomes 0600.
- `TestProxyHomeFollowsTheConfigHome` — after `IsolateConfigHomeForTest`,
  `ProxyHomePath()` is under the scratch `XDG_CONFIG_HOME` and the real home
  is untouched.

Run: `go -C wt test ./internal/config -run 'TestWriteConfigFile|TestProxyHomeFollows' -count=1`
Expected: FAIL

- [ ] **Step 2: implement the mode rule and the guard change**

`writeConfigFile` computes 0600 when `[litellm].api_key` **or** the
`[model-server]` inline token is non-empty; when the target file exists and its
mode is stricter than the computed one, keep the stricter mode (stat first).
`bin/check-config-dirs-untouched`: `for name in agent-wt local-ai litellm wt-model; do`
(line 27) and the header prose names `wt-model`.

- [ ] **Step 3: run the checks**

Run: `go -C wt test ./internal/config -count=1`; `make lint-shell`; then
`bash bin/check-config-dirs-untouched` from the repo root (it must pass on
this machine only if the real dirs do not exist — expected to fail locally on a
dev machine with `::error::` output; that is its documented behavior, not a
regression).
Expected: tests PASS, `lint-shell` clean

- [ ] **Step 4: commit**

```bash
git add wt/internal/config bin/check-config-dirs-untouched
git commit -m "fix(config): secret-bearing mode for the inline token, never loosen; guard wt-model in CI"
```

---

### Task 5: docs, spec amendments, style guide, sweep

**Files:**
- Modify: `docs/superpowers/specs/2026-11-15-wt-model-session-split-design.md` (:75 session-side table "removed" → read-only legacy fallback; :77 Command homes: `set|status` live in **both** names in stage 1 and the home is named; :133 the Phase 4 deferral marks `wt-model litellm set` + config home done), root `CLAUDE.md` (:47, :48), `wt/CLAUDE.md` (:102, :104, :107, :111, :195, :228-235, :311-314), `wt/docs/wt-model-server.md` (:6-8, :21-23, :59-62, + a "Proxy url and key" section with the read-once rule and the key-file rules; the token-file section :64-89 is the wording precedent), `wt/docs/wt-session.md` (:179-202, :204-211), `wt/docs/wt-agents/README.md` (:10-12, :20-24, :38-44), `docs/guides/00-config-map.md` (new `~/.config/wt-model/` rows + :21, :171, :341), `docs/guides/04-litellm-config.md` (:216, :344-345, :348, :362, :369), `docs/guides/06-wt-agents-and-models.md` (:112, :116-117), `docs/guides/13-deployment-use-cases.md` (:10, :77), `docs/guides/08-maintenance-and-troubleshooting.md` (:500, :551, :561-572), `wt/docs/style-guide.md` (S2 :173, S7 :178, M6 :44)
- Test: none (docs task); gates are `bin/check-links` and the greps.

**Interfaces:**
- Consumes: the exact strings Tasks 1–3 settled (note line, refusals, file
  names `~/.config/wt-model/config.toml` / `litellm.key`, fixture paths).
- Produces: nothing downstream; this is the last task.

- [ ] **Step 1: amend the split spec and the CLAUDE.md files**

Per the files list. The dated `2026-11-15-wt-session-client.md` plan stays
historical (Ruling 15). Guides never embed live model state (D4) — show
commands, not snapshots.

- [ ] **Step 2: the reference pages and the guides**

Per the files list. `wt-model-server.md`'s new section states: where the pair
lives, the key-file rules (`chmod 600` sentence, `umask 077` recipe as the
hand-creation precedent), the read-once rule ("a running server keeps the pair
it started with; restart it after `wt-model litellm set`"), and the legacy
fallback + note. Exit-code prose in `Long` texts and doc pages stays equal (C2).

- [ ] **Step 3: style guide and the sweep**

Update S2/S7/M6's quoted examples to `wt-model litellm set --url URL --api-key KEY`
and the key's source name. Then the D3 sweeps:
`grep -rn "wt litellm set" --include='*.md' .` (every doc hit names the new
command or deliberately quotes history), `grep -rn "UpdateLitellm\|LitellmConfigured" .`
(must be gone), and `grep -rn "\[litellm\]" docs/guides wt/docs wt/CLAUDE.md ../CLAUDE.md 2>/dev/null | grep -v read-only`
as the stale-claim pass.

- [ ] **Step 4: verify links and commit**

Run: `make check-links` (repo root) and `make -C wt check`
Expected: clean

```bash
git add docs CLAUDE.md wt/CLAUDE.md wt/docs
git commit -m "docs: the wt-model config home (Phase 4 stage 1) across guides, pages and the split spec"
```
