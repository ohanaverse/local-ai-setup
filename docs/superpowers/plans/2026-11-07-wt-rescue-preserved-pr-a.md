# `wt rescue` Record (PR A: inert) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** After a verified `wt rescue`, leave an immutable record of the source (`refs/wt/rescued/<id>`: a two-commit chain holding its index tree and working-tree tree), built by a new `internal/dirtsnap` package, and report it in the rescue result. Nothing in `wt cleanup` or `wt remove` changes.

**Architecture:** `internal/dirtsnap` builds the working-tree tree id with git plumbing and no temp file (so it passes the repo's deletion scan). `internal/rescue` calls it after `verify()` passes, wraps the two trees in commits parented on the source's HEAD, and creates the ref create-only. A write failure never fails the rescue: it adds a `record_not_written: ...` warning. The rescue-result document gains an additive `record {ref, commit}` object.

**Tech Stack:** Go (module `wt/`), git plumbing (`ls-tree`, `hash-object`, `write-tree`, `commit-tree`, `update-ref`), the existing JSON-schema contract tests.

**Spec:** `docs/superpowers/specs/2026-11-07-wt-rescue-preserved-by-design.md` ("The record", "`internal/dirtsnap`", "Failure behavior", "PR A: inert"). PR B1 and B2 are separate plans. Go and test paths below are relative to `wt/`; a path starting with `wt/` or `docs/` is from the repository root.

## Global Constraints

- Inert: every `wt cleanup` and `wt remove` output is byte-identical to today. No change to `internal/cleanup`, `internal/removal` or `internal/exitprompt`.
- Fail closed: a record that cannot be written leaves the rescue `moved` with a warning and no ref; no record exists after any refusal or failure.
- No deletion code: `TestDeletionStringsOnlyInRemovalPackage` passes with **no** new allow-list entry; `fileOpNever` covers `internal/rescue/`. `dirtsnap` creates no temp file and deletes nothing (test files may).
- Ref: `refs/wt/rescued/<first 16 hex of W's sha>`, created with `git update-ref <ref> <W> ""` (create-only). An existing ref at the same commit counts as written; at another commit it is a write failure.
- Commit I: tree = the source's `git write-tree`, parent = source HEAD. Commit W: tree = `dirtsnap` working-tree tree, parent = I. Identity from environment: `GIT_AUTHOR_NAME`/`GIT_COMMITTER_NAME` = `wt rescue`, `GIT_AUTHOR_EMAIL`/`GIT_COMMITTER_EMAIL` = `wt@invalid`, real clock.
- Trailers (in both commits' messages): `Wt-Version: 1`, `Wt-Source-Path` (symlink-resolved, `cleanup.ResolvePath`), `Wt-Source-Ref` (full refname, or `detached`), `Wt-Source-Head`, `Wt-Dest-Branch`, `Wt-Dest-Path`. A value with a control character means no record (write failure).
- Written only after `verify()` passed and the source fingerprint was equal before and after the copy; the fingerprint is retaken once more just before writing.
- `dirtsnap` runner type: `func(stdin []byte, args ...string) ([]byte, error)` (avoids an import cycle with `internal/cleanup`). Tree entries use mode `40000` for a tree (not `040000`).
- Warning line prefix is exactly `record_not_written: `. The rescue-result `code` enum does not change. `EnvelopeVersion` stays 1.
- Every `Test*` has a top-level comment saying what and why; a package that can run git has `gitenv.IsolateForTest()` in `TestMain`; scratch repo per case (`wt/CLAUDE.md` "Go tests").
- PR body says `Refs #532`, never a closing keyword.

## Review Focus

The inputs the spec implies that no task would otherwise exercise, most likely first; each has its test in the named task:

1. A path with a space, non-ASCII bytes or a newline (`dirtsnap` trees, trailers): the record is still equal to the oracle and readable (Task 2, Task 3).
2. A machine with no git identity (`user.useConfigOnly=true`, no global config): the record is still written (Task 3).
3. A second rescue of the same unchanged source: two valid, distinct records, neither disturbs the other (Task 4).
4. Ignored files and a `.gitignore`d directory: not in the record, matching the oracle (Task 2).
5. The source edited between verify and the write: no record, a warning, the rescue still `moved` (Task 4).

## File Structure

- Create `internal/dirtsnap/dirtsnap.go` — `Build`, `Runner`, `Limits`, sentinel errors; `internal/dirtsnap/tree.go` — tree entry parse/encode and the in-memory tree merge; tests `dirtsnap_test.go`, `oracle_test.go`, `testmain_test.go`.
- Create `internal/rescue/record.go` — `writeRecord`, the ref seam, the trailer builder; test `record_test.go`.
- Modify `internal/rescue/git.go` (`sgit` gains an env), `types.go` (`Record`, `Outcome.Record`, prefix constants), `execute.go` (write after verify), `json.go` (`RecordJSON`, `Envelope.Record`).
- Modify `cmd/wt/rescue.go` (one text line) and the contract files `docs/contracts/rescue-result.schema.json`, `docs/contracts/rescue-result.sample.json`.
- Docs: `wt/CLAUDE.md`, `wt/docs/wt-cleanup.md`, `wt/docs/internals/packages.md`, `wt/CHANGELOG.md`.

---

### Task 1: `dirtsnap` builds the tree for regular files

**Files:**
- Create: `internal/dirtsnap/dirtsnap.go`, `internal/dirtsnap/tree.go`, `internal/dirtsnap/testmain_test.go`, `internal/dirtsnap/oracle_test.go`, `internal/dirtsnap/dirtsnap_test.go`

**Interfaces:**
- Produces:
  - `type Runner func(stdin []byte, args ...string) ([]byte, error)` — runs git in the repository; a non-zero exit is an error.
  - `type Limits struct{ MaxFiles int; MaxBytes int64 }` — zero means unlimited.
  - `var ErrUnsupported, ErrTooLarge error` — wrapped with `%w`.
  - `func Build(dir string, run Runner, write bool, lim Limits) (tree string, err error)` — the 40-hex tree id of HEAD plus the working changes (staged, unstaged, untracked non-ignored). `write` adds `-w` to every `hash-object` (rescue); false writes nothing (cleanup's later read path). HEAD must exist.

- [ ] **Step 1: `testmain_test.go`** with `gitenv.IsolateForTest()` (copy the shape of `internal/rescue/testmain_test.go`, minus the cleanup stub). `oracle_test.go`: `func oracleTree(t *testing.T, dir string) string` — in a temp `GIT_INDEX_FILE` run `git read-tree HEAD`, `git add -A`, `git write-tree`; and `func newRepo(t *testing.T) string` (fresh `t.TempDir()` repo with one commit containing `a.txt`, `x/y.txt`, an executable `run.sh`). A real runner for tests: `func runIn(dir string) Runner` using `exec.Command("git", "-C", dir, ...)` with stdin.
- [ ] **Step 2: write the failing tests** in `dirtsnap_test.go`:
  - `TestBuildOfACleanTreeIsHeadsTree` asserts `Build(...) == git rev-parse HEAD^{tree}`.
  - `TestBuildMatchesTheTempIndexOracle` — table, each case mutates the repo then asserts `Build(dir, runIn(dir), true, Limits{}) == oracleTree(t, dir)`: modified tracked file; new top-level untracked file; tracked file deleted from disk; exec bit flipped on `run.sh`; new nested untracked directory `n/m/o.txt`; a staged-only change; an entire tracked directory `x/` deleted.
- [ ] **Step 3: run** `go test ./internal/dirtsnap` — expect FAIL (`Build` undefined).
- [ ] **Step 4: implement `Build`.** Algorithm (not determined by the signature): read HEAD's tree id; run `status --porcelain=v1 -z --untracked-files=all --no-renames --ignore-submodules=none` and, for each listed path, take the on-disk state with `os.Lstat`: a regular file becomes a blob (`hash-object --no-filters -t blob [-w] --stdin`, bytes on stdin) with mode `100644` or `100755` by the owner exec bit; a missing file removes the entry. Apply removals before additions. Rebuild only the trees along touched paths: load a tree's entries with `ls-tree -z <id>` (convert the printed `040000` to `40000`), merge in memory, drop empty trees, emit raw entries (`<mode> <name>\0<20 raw bytes>`) in git's order (a tree sorts as its name plus `/`), hash with `hash-object -t tree [-w] --stdin`. Code in `tree.go`: `parseLsTree`, `encodeTree`, `mergeTree`.
- [ ] **Step 5: run** `go test ./internal/dirtsnap` — expect PASS; `gofmt -l .`; `go vet ./internal/dirtsnap`.
- [ ] **Step 6: commit** — `feat(wt): dirtsnap builds the working-tree tree id without a temp index`.

### Task 2: `dirtsnap` edge cases, write modes and limits

**Files:**
- Modify: `internal/dirtsnap/dirtsnap.go`, `internal/dirtsnap/tree.go`, `internal/dirtsnap/dirtsnap_test.go`

**Interfaces:**
- Consumes: Task 1's `Build`, `runIn`, `oracleTree`, `newRepo`.
- Produces: `Build` also returns `ErrUnsupported` for a nested repository, a submodule path or a special file (fifo, socket, device), and `ErrTooLarge` when changed files or bytes exceed `lim`.

- [ ] **Step 1: write the failing tests:**
  - `TestBuildMatchesTheOracleOnTrickyShapes` — table against `oracleTree`: a tracked file replaced by a directory; a tracked directory replaced by a file; a new and a changed symlink; siblings `a`, `a.b` and directory `a/` together; a file named with spaces, non-ASCII bytes and (separate case) a newline; an ignored file and a `.gitignore`d directory are absent from the tree (the oracle respects the same `.gitignore`); a mode `0600` untracked file (oracle also records `100644`).
  - `TestBuildWriteFalseWritesNothing` — add a file with new content; compute its blob id with `git hash-object --stdin` (no `-w`); after `Build(..., false, ...)` `git cat-file -e <blob>` fails and the tree object is absent; after `Build(..., true, ...)` both exist.
  - `TestBuildRefusesWhatATreeCannotHold` — a nested git repo directory, a submodule gitlink path and a fifo each give `errors.Is(err, ErrUnsupported)`.
  - `TestBuildStopsAtTheLimits` — three changed files with `Limits{MaxFiles: 2}` and a 10-byte file with `Limits{MaxBytes: 5}` each give `errors.Is(err, ErrTooLarge)`; `Limits{}` builds.
- [ ] **Step 2: run** — expect FAIL on the cases the Task 1 code does not handle.
- [ ] **Step 3: extend `Build`.** Symlink: blob content is the link target, mode `120000`. A path that is a directory on disk (nested repo) or a non-regular, non-symlink file: `ErrUnsupported`. A gitlink in HEAD's tree whose path appears in status: `ErrUnsupported`. Count files and bytes of changed (hashed) paths against `lim` before hashing.
- [ ] **Step 4: run** `go test ./internal/dirtsnap` — expect PASS.
- [ ] **Step 5: commit** — `feat(wt): dirtsnap covers links, replacements, odd names, limits`.

### Task 3: the record writer

**Files:**
- Create: `internal/rescue/record.go`, `internal/rescue/record_test.go`
- Modify: `internal/rescue/git.go`, `internal/rescue/types.go`

**Interfaces:**
- Consumes: `dirtsnap.Build(dir, run, true, dirtsnap.Limits{})`; `sgit` from `git.go`; `Plan` (Source, Branch, DestPath, Start).
- Produces:
  - `sgit` gains `env []string`, passed as `cleanup.Git.Env` in `runner()`.
  - `type Record struct{ Ref, Commit string }` in `types.go`; `const RecordRefPrefix = "refs/wt/rescued/"`; `const RecordWarningPrefix = "record_not_written: "`.
  - `func writeRecord(src sgit, p *Plan) (Record, error)` in `record.go`: builds I and W, returns `Record{Ref, Commit: W}`.
  - `var updateRecordRef = func(src sgit, ref, commit string) error` — the seam: `git update-ref <ref> <commit> ""`; if that fails and `rev-parse --verify -q <ref>` equals `commit`, nil; otherwise the error.

- [ ] **Step 1: write the failing tests** in `record_test.go` (set up with `f := newFixture(t)`, `p := f.Worktree("topic")`, commits and edits, then `eng, find := f.engine(f.Dir)`, `plan, err := eng.Preflight(find(p), "")`, and call `writeRecord(sgit{dir: p}, plan)` directly; add a local `oracleTree` helper like Task 1's):
  - `TestWriteRecordBuildsTheChain` — `Record.Ref` has the prefix plus the first 16 hex of `Commit`; `rev-parse <ref>` equals `Commit`; `Commit^` is I and `I^` is the source HEAD; I's tree equals `git write-tree` in the source; W's tree equals the oracle; all six trailers present and correct via `git log -1 --format=%(trailers:key=Wt-Source-Path,valueonly)` (and the others); for a detached source `Wt-Source-Ref` is `detached`.
  - `TestWriteRecordUsesItsOwnIdentity` — set `user.useConfigOnly=true` in the repo config (TestMain already sets no global config): the record is still written and its author is `wt rescue <wt@invalid>`.
  - `TestWriteRecordRefusesAControlCharacter` — mutate `plan.DestPath` to contain `\n`: error, and `for-each-ref refs/wt` is empty and no new commit objects are reachable from any ref.
  - `TestWriteRecordHandlesAPathWithANewlineOrNonASCII` — a source file named with a newline and one with `é` (untracked): W's tree equals the oracle.
  - `TestUpdateRecordRefIsCreateOnly` — twice with the same commit: nil; with a different commit at an existing ref: error and the ref unchanged.
  - `TestWriteRecordReadsButNeverChangesTheSource` — `snapshot(t, p)` before and after are equal (`diff == ""`).
- [ ] **Step 2: run** `go test ./internal/rescue -run 'WriteRecord|UpdateRecordRef'` — expect FAIL (undefined).
- [ ] **Step 3: implement.** `writeRecord`: reject control characters in `Source.Path`, `Source.Ref`, `Branch`, `DestPath` (rune < 0x20 or 0x7f); `src.text("write-tree")` for I's tree (an unmerged index makes it fail: that is the error); `dirtsnap.Build(src.dir, src.out, true, dirtsnap.Limits{})` for W's tree (adapt `sgit.out` to the `Runner` type); commits via `git commit-tree <tree> -p <parent> -F -` with the message on stdin and the identity env on a second `sgit{dir, env}`; ref id from W; then `updateRecordRef`. Resolve the path with `cleanup.ResolvePath`. Message: `wt rescue record`, blank line, the six trailers.
- [ ] **Step 4: run** the package tests — expect PASS; `gofmt -l .`; `go vet ./internal/rescue`.
- [ ] **Step 5: commit** — `feat(wt): write the rescue record chain`.

### Task 4: write the record after verify

**Files:**
- Modify: `internal/rescue/execute.go`, `internal/rescue/types.go`
- Test: `internal/rescue/execute_test.go` (new tests appended), `internal/rescue/fixture_test.go`

**Interfaces:**
- Consumes: Task 3's `writeRecord`, `updateRecordRef`, `RecordWarningPrefix`; the existing `rescueOnce(t, f, src)`, `snapshot`, `afterCarry` seam style.
- Produces: `Outcome.Record *Record` (set only on `StatusMoved` when written); `var afterVerify = func(p *Plan) {}` — a no-op seam called after `verify()` and before the fingerprint retake.

- [ ] **Step 1: write the failing tests** (comment each with what and why):
  - `TestExecuteWritesARecordAfterVerify` — for commits-only, staged, unstaged, nested-untracked and detached sources (use `f.Detached`): `o.Status == StatusMoved`; `o.Record != nil`; `rev-parse o.Record.Ref == o.Record.Commit`; W's tree equals the oracle of the source's state at rescue; I's tree equals the source's `write-tree`; `snapshot` before/after equal.
  - `TestExecuteWritesNoRecordWhenNothingMoved` — a refusal (`CodeNothingToKeep` on a clean source), `CodeNameUnavailable`, and a failed verify (copy the seam used by `TestExecuteFailsWhenTheCopyDiffers`): `for-each-ref refs/wt/rescued` is empty each time and `o.Record == nil`.
  - `TestExecuteKeepsMovedWhenTheRecordCannotBeWritten` — swap `updateRecordRef` to return an error: `StatusMoved`, `o.Record == nil`, `o.Plan.Warnings` has exactly one line starting `RecordWarningPrefix`, no ref exists.
  - `TestExecuteWritesNoRecordWhenTheSourceChangedAfterVerify` — `afterVerify = func(p *Plan) { edit the source }`: `StatusMoved`, no record, a warning starting with `RecordWarningPrefix` naming the change.
  - `TestTwoRescuesOfOneSourceLeaveTwoValidRecords` — rescue once, then `eng.Preflight(row, "rescued/second")` and Execute: two refs under the prefix, both resolve, their `Wt-Dest-Branch` trailers differ.
- [ ] **Step 2: run** — expect FAIL.
- [ ] **Step 3: implement.** In `Execute`, after `done("verified ...")`: call `afterVerify(p)`; `again, err := takeFingerprint(sg)`; if `err != nil` or `p.fp.diff(again) != ""` add the warning `RecordWarningPrefix + "the source changed after the copy was verified"` (or the read error) and skip; else `writeRecord(sg, p)`; on error append `RecordWarningPrefix + cleanup.OneLine(err.Error())` to `o.Plan.Warnings`; on success set `o.Record` and `done("recorded the source at " + rec.Ref)`. Then `o.Status = StatusMoved`.
- [ ] **Step 4: run** `go test ./internal/rescue` — expect PASS (all existing rescue tests included).
- [ ] **Step 5: commit** — `feat(wt): wt rescue leaves a record after a verified copy`.

### Task 5: the rescue-result contract

**Files:**
- Modify: `internal/rescue/json.go`, `docs/contracts/rescue-result.schema.json`, `docs/contracts/rescue-result.sample.json`, `cmd/wt/rescue_contract_test.go`

**Interfaces:**
- Consumes: `Outcome.Record`.
- Produces: `type RecordJSON struct{ Ref string \`json:"ref"\`; Commit string \`json:"commit"\` }`; `Envelope.Record *RecordJSON \`json:"record,omitempty"\``; `EncodeResult` fills it from `o.Record`.

- [ ] **Step 1: write the failing test change.** In `TestRescueEncodersProduceSchemaValidDocuments` give the moved outcome a `Record: &rescue.Record{Ref: rescue.RecordRefPrefix + "0123456789abcdef", Commit: <40 hex>}`. Add `Record` to `Envelope` first (Step 3) to see `TestRescueSchemaPropertiesMatchGoTags`, `TestRescueFixturesMatchEnvelopeShape` and `TestRescueEveryOptionalKeyAppears` fail until the schema and sample move.
- [ ] **Step 2: run** `go test ./cmd/wt -run Rescue -count=1` — expect FAIL on the three contract tests.
- [ ] **Step 3: implement** `RecordJSON` and `Envelope.Record` (absent when nil). Schema: a `$defs/record` (`ref`, `commit`, both required, `additionalProperties: false`), a `record` property, and the `warnings` description gains: a line starting `record_not_written: ` means the rescue was verified but left no record. Sample `rescue-result.sample.json` (outcome `moved`) gains `"record": {"ref": "refs/wt/rescued/<16 hex>", "commit": "<40 hex>"}` and the matching `completed` line. Leave the schema's top-level sentence "moved does NOT make the source removable" as is (still true in this PR).
- [ ] **Step 4: run** `go test ./cmd/wt -run Rescue -count=1` — expect PASS.
- [ ] **Step 5: commit** — `feat(wt): rescue-result carries the record`.

### Task 6: the text line

**Files:**
- Modify: `cmd/wt/rescue.go` (`printRescueText`)
- Test: `cmd/wt/rescue_test.go`

- [ ] **Step 1: failing test** `TestPrintRescueTextNamesTheRecord` (what: a moved outcome with a Record prints one line `    record: <ref> (holds the commits and the working state)` and a moved outcome without one prints no `record:` line; why: the user must be able to find what a rescue left, and the line must not appear when nothing was written). Model it on `TestPrintRescueTextNamesAKeptBranch`.
- [ ] **Step 2: run** — expect FAIL.
- [ ] **Step 3: implement** in `printRescueText`, after the `done:` lines and before the warnings, when `o.Record != nil`.
- [ ] **Step 4: run** `go test ./cmd/wt -run PrintRescueText -count=1` — expect PASS.
- [ ] **Step 5: commit** — `feat(wt): wt rescue names the record it left`.

### Task 7: the record survives what the destination and branch do not

**Files:**
- Test: `internal/rescue/record_test.go` (append)

- [ ] **Step 1: write the test** `TestTheRecordOutlivesTheBranchTheReflogAndGC` (what: after a rescue, delete the rescue branch and its worktree with plain git, expire every reflog, run `git gc --prune=now`; the record ref still resolves, `cat-file -e` finds I and W, and `git show <W>:<an untracked file>` still prints its content; why: "preserved" must not depend on the destination, and a bare hash with no ref would be pruned). Test code may use `git branch -D`, `worktree remove --force`, `reflog expire --expire=now --all`, `gc --prune=now`.
- [ ] **Step 2: run** `go test ./internal/rescue -run OutlivesTheBranch` — expect PASS (the behavior exists from Task 4; this pins it).
- [ ] **Step 3: run** `go test ./internal/removal -run TestDeletionStringsOnlyInRemovalPackage -count=1` and `go test ./cmd/wt -run TestRescueContainsNoDeletion -count=1` — expect PASS with no allow-list change.
- [ ] **Step 4: commit** — `test(wt): the rescue record survives branch deletion and gc`.

### Task 8: docs

**Files:**
- Modify: `wt/CLAUDE.md` (Go module package list: add `dirtsnap`; Rescue invariants: a bullet on the record), `wt/docs/wt-cleanup.md` (rescue section: the record line and the manual removal command `git update-ref -d refs/wt/rescued/<id>`, with the note that records pin objects until removed and `wt` does not prune them yet), `wt/docs/internals/packages.md` (a row for `internal/dirtsnap/`, and `record.go` in the rescue row), `wt/CHANGELOG.md` (Added: rescue leaves a record).

- [ ] **Step 1: edit** the four files. The Rescue invariants bullet states: ref and chain, written after verify only, failure is a `record_not_written:` warning, `dirtsnap` needs no temp file (deletion scan), nothing reads the record yet (inert until PR B1). Do **not** change the sentence that the original is not made removable (still true) or the help text.
- [ ] **Step 2: run** `bin/check-links` from the repo root — expect `ALL LINKS OK`.
- [ ] **Step 3: commit** — `docs(wt): the rescue record and dirtsnap`.

### Task 9: acceptance on the built binary

- [ ] **Step 1:** `D=$(mktemp -d); go build -o "$D/wt" ./cmd/wt` (do not `make install`).
- [ ] **Step 2:** in a scratch repo with a bare `origin`: a worktree with an unpushed commit, a staged file, an unstaged edit, a nested untracked directory with a `0700` file, an ignored file. Run `"$D/wt" rescue <worktree> --yes --json`. Expect outcome `moved`, a `record` object; `git for-each-ref refs/wt/rescued` lists it; `git cat-file -p <commit>` shows the trailers; `git ls-tree -r <commit>` lists the untracked file and not the ignored one; the original's `git status` and file bytes are unchanged.
- [ ] **Step 3:** `git branch -D rescued/<name>`, `git worktree remove --force` the destination, `git reflog expire --expire=now --all`, `git gc --prune=now`: the record still prints its files.
- [ ] **Step 4:** `wt cleanup --json` and `wt remove <original> --dry-run` give the same rows and approvals as before the rescue (inert). Run the full gate from `wt/`: `gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`, `make check`, `make -C .. check-links`.
- [ ] **Step 5:** report commands and outputs for the PR body; the PR says `Refs #532`.

---

## Self-review

- **Spec coverage:** dirtsnap with oracle, two modes, tree-mode trap, size cap, runner type (Tasks 1, 2); the ref, both commits, trailers, identity, create-only, control characters, write-after-verify, fingerprint retake (Tasks 3, 4); failure behavior and the warning prefix (Task 4); the additive `record` object, schema, sample, `EnvelopeVersion` 1 (Task 5); the text line (Task 6); gc survival and the deletion scan (Task 7); docs and the manual pruning command (Task 8); acceptance (Task 9). Deliberately not in this plan: anything in `internal/cleanup`, `internal/removal`, the help text and the schema's "not removable" sentence (B1), pruning (follow-up issue).
- **Type consistency:** `Record{Ref, Commit}`, `RecordRefPrefix`, `RecordWarningPrefix`, `writeRecord(src sgit, p *Plan) (Record, error)`, `updateRecordRef`, `afterVerify`, `dirtsnap.Build(dir, run, write, lim)` are used with the same names in every task.
- **Proportion:** bodies appear only for the tree-merge algorithm; every other step is a signature, a test name with assertions, or a command.
