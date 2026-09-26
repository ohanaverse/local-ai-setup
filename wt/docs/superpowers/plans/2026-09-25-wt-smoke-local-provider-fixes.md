# wt smoke / local-provider fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the three root causes found while debugging `wt smoke` against the local providers (mtplx, omlx, ollama): copilot's tool calls get silently denied during one-shot smoke runs, a stuck-warmup omlx row gives no diagnostic, and LiteLLM's `ollama_chat` bridge 500s for `gpt-oss:20b` because of a bad `frequency_penalty`/`presence_penalty` translation.

**Architecture:** Three independent, narrowly-scoped Go changes in the `wt` module, each with its own test. No new packages or abstractions — each fix lands at the exact call site the investigation identified.

**Tech Stack:** Go (wt module), `gopkg.in/yaml.v3` (LiteLLM config editing), the existing `env`/seam test patterns in `internal/lifecycle` and `internal/smoke`.

**Spec:** None — this plan implements fixes derived from a live debugging investigation (systematic-debugging session, 2026-09-25) rather than a pre-existing spec. The investigation's findings are summarized in each task below in place of spec citations.

## Global Constraints

- Every `Test*` needs a `//` comment stating what it tests and why it matters (wt/CLAUDE.md's Go-tests convention).
- `internal/litellm`'s `additional_drop_params`/`use_chat_completions_api` enforcement is **presence-based, never merged**: if the key exists at all (even a shorter or empty list), `EnsureSettings` leaves it alone — this is a tested, deliberate contract (`TestSetRowPreservesUserManagedParams`) protecting a user's deliberate opt-out. Task 3 must not weaken this.
- `wt smoke` never writes modelman-owned state or flips LiteLLM routing (wt/docs/wt-smoke.md) — none of these fixes touch that boundary.
- Run `go build ./... && go vet ./... && go test ./...` from `wt/` (module root) before every commit, per wt/CLAUDE.md.
- Config-file edits to the live `~/.config/litellm/config.yaml` are operational steps, not code — they use the same manual-edit-plus-restart flow already documented in `wt/docs/wt-agents/litellm-troubleshooting.md`.

## Review Focus

- **A copilot smoke run against a model whose backing LLM decides to run a genuinely destructive command.** `--yolo` now applies to `wt smoke`'s one-shot copilot launches; the prompt is fixed and trivial ("reply with exactly this text"), but nothing stops a future `--prompt` override from combining with an unsupervised agent. Task 1 documents this trade-off explicitly in code and docs rather than leaving it implicit.
- **A warmup failure whose response body is huge (an HTML error page, a stack trace).** The new diagnostic must truncate before embedding it in the returned error, or a single warmup failure could balloon `wt smoke`'s captured output.
- **A live `config.yaml` row whose `additional_drop_params` already has one entry (`[reasoning_effort]`) from before this change.** `EnsureSettings` must leave it exactly as-is (presence-based), matching the existing pinned test — Task 3 adds a dedicated test for this so the "only new rows get the wider list" behavior doesn't regress into a silent merge (or, worse, a silent skip that never fixes anything).
- **A non-`ollama_chat` row (`openai/*`, `anthropic/*`, `openrouter/*`) must be unaffected** by the wider drop list — only the `ollama_chat/` prefix branch changes.
- **The warmup loop's health-check-not-responding branch** (server not up yet) must keep producing its own distinct reason string, separate from a chat-completion failure reason, so a cold-starting server doesn't get misreported as "model not found" from a stale prior iteration's reason.

---

### Task 1: `wt smoke` grants tool-use permission for one-shot agent launches

**Files:**
- Modify: `wt/internal/smoke/smoke.go:278`
- Modify: `wt/docs/wt-smoke.md`
- Modify: `wt/CLAUDE.md` (Smoke test section)
- Test: `wt/internal/smoke/smoke_test.go`

**Interfaces:**
- Consumes: `agents.BuildLaunchCmd(agent string, m config.Model, worktreePath string, yolo bool, sess *session.Session, cfg *config.Config, extraArgs []string) (*exec.Cmd, error)` (existing, unchanged signature).
- Produces: nothing new — this task only changes one argument at one call site.

**Root cause (from the investigation):** `realBuildAndRun` in `smoke.go` calls `agents.BuildLaunchCmd(agentName, m, cwd, false, nil, cfg, nil)` — `yolo` is hardcoded `false`. GitHub Copilot CLI's own `--help` documents `--allow-all-tools`/`--yolo` as **"required for non-interactive mode."** Without it, any tool call copilot's backing model attempts during the one-shot `-p` run gets `Permission denied and could not request permission from user` (no TTY to approve it). Live evidence (`/tmp/local-ai-setup-mtplx.log`, `wt smoke` run `run-6213ded0` against an mtplx model): given only the trivial "reply with exactly this text" prompt, the model tried to explore the repo and run commands (`codesign`, `go run`, the `wt` binary), got denied every time, and narrated the denials as a "macOS security restriction" — burning ~200K prompt tokens over 1118 seconds before finally complying, well past `wt smoke`'s default 900s local timeout. Under normal conditions this row FAILs on timeout with that misleading narrative in its captured output. The same run against `ollama/qwen3.8:27b-mlx` passed cleanly because that model never attempted a tool call for the trivial prompt — the missing flag simply never got exercised.

- [x] **Step 1: Write the failing test**

Add to `wt/internal/smoke/smoke_test.go`, next to `TestRealBuildAndRunOneShotArgsNilApplier`:

```go
// TestRealBuildAndRunPassesYolo pins that wt smoke's one-shot launches grant
// tool-use permission (copilot: --yolo). Without it, copilot CLI's own docs
// say non-interactive mode ("-p") cannot get tool-call approval at all — any
// tool call the backing model attempts is denied outright
// ("Permission denied and could not request permission from user"), which a
// less rigidly instruction-following model can spiral on for many minutes
// before giving up or timing out (observed live against an mtplx model,
// 2026-09-25: ~1118s and ~200K tokens burned narrating the denials as a
// "macOS security restriction" before it finally answered the trivial
// smoke prompt). A regression here would reintroduce that failure mode
// silently, since a well-behaved model (as in the ollama case that passed)
// never exercises the missing flag.
func TestRealBuildAndRunPassesYolo(t *testing.T) {
	writeFakeAgentBinary(t, "copilot")
	var cleanup func() error
	outcome := realBuildAndRun(&config.Config{}, "copilot", config.Model{Native: true}, "the prompt", t.TempDir(), time.Second, nil, &cleanup)
	if !strings.HasSuffix(outcome.Command, "--yolo -p the prompt") {
		t.Fatalf("Command = %q, want it to end with \"--yolo -p the prompt\" (yolo must be passed for one-shot copilot launches)", outcome.Command)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `cd wt && go test ./internal/smoke -run TestRealBuildAndRunPassesYolo -v`
Expected: FAIL — `Command = ".../copilot -p the prompt"` (no `--yolo`).

- [x] **Step 3: Fix the call site**

In `wt/internal/smoke/smoke.go`, change line 278 from:

```go
	cmd, err := agents.BuildLaunchCmd(agentName, m, cwd, false, nil, cfg, nil)
```

to:

```go
	// yolo=true: a one-shot smoke prompt must never stall on an
	// interactive tool-permission prompt it has no TTY to answer — see
	// TestRealBuildAndRunPassesYolo for the failure this prevents.
	cmd, err := agents.BuildLaunchCmd(agentName, m, cwd, true, nil, cfg, nil)
```

- [x] **Step 4: Run test to verify it passes**

Run: `cd wt && go test ./internal/smoke -run TestRealBuildAndRunPassesYolo -v`
Expected: PASS

- [x] **Step 5: Run the full smoke package's test suite to check for regressions**

Run: `cd wt && go test ./internal/smoke -v`
Expected: PASS (all tests, including the existing `TestRealBuildAndRunOneShotArgsNilApplier`/`...PlacedByApplier`/`...RevertsAndReappends...` tests, which use `agy` and are unaffected by copilot's yolo behavior but must still pass since `yolo=true` is now global to every agent's smoke launch).

- [x] **Step 6: Update `wt/docs/wt-smoke.md`**

Add a new bullet under the existing `## Eligibility` section (after the paragraph ending "...`smoke.Candidates` walks the same `catalog` rows a real launch consults)."):

```markdown
- **Tool-use permission.** Every one-shot launch runs with the agent's
  yolo/`--allow-all-tools`-equivalent flag set, regardless of the root
  `--yolo` flag's own state. A one-shot prompt has no TTY to answer an
  interactive tool-permission prompt; without this, a backing model that
  attempts any tool call during the trivial smoke prompt gets a
  permission-denied response it may not recover from within the timeout
  (observed with copilot CLI, whose own docs call `--allow-all-tools`
  "required for non-interactive mode").
```

- [x] **Step 7: Update `wt/CLAUDE.md`'s Smoke test section**

In the `## Smoke test (\`wt smoke\`)` section, after the sentence ending "...reporting PASS/FAIL/SKIP.", add:

```markdown
Every one-shot launch runs with the agent's yolo flag forced on
(`agents.BuildLaunchCmd(..., yolo=true, ...)` in `internal/smoke/smoke.go`,
independent of the root `--yolo` flag) — a one-shot prompt has no TTY to
answer an interactive tool-permission prompt, and without this a backing
model that attempts a tool call for the trivial smoke prompt can spiral on
repeated permission denials for many minutes instead of failing fast (see
`TestRealBuildAndRunPassesYolo`).
```

- [x] **Step 8: Commit**

```bash
cd wt
git add internal/smoke/smoke.go internal/smoke/smoke_test.go docs/wt-smoke.md CLAUDE.md
git commit -m "$(cat <<'EOF'
fix(wt): wt smoke grants tool-use permission for one-shot agent launches

Copilot CLI's own docs mark --allow-all-tools "required for non-interactive
mode"; wt smoke hardcoded yolo=false, so any tool call a backing model
attempted during the trivial smoke prompt was denied outright. A less
instruction-following model (observed live with an mtplx model) spiraled on
the denials for over 18 minutes, burning ~200K tokens narrating them as a
macOS security restriction, well past the 900s default local timeout.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Warmup failures surface their reason instead of failing silently

**Files:**
- Modify: `wt/internal/lifecycle/probe.go`
- Test: `wt/internal/lifecycle/probe_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `(e *env) tryChat(ctx context.Context, chatURL string, payload []byte) (ok bool, reason string)` — signature change from `(e *env) tryChat(...) bool`; only caller is `(e *env) warmup` in the same file, so no other package is affected.

**Root cause (from the investigation):** `registry.toml`'s `omlx/mlx-community--Qwen3.8-27B-8bit` points at an on-disk model directory that is an **incomplete download** (6 of its weight shards are missing — confirmed via omlx's own `model_discovery` warning, present on every server startup in `/opt/homebrew/var/log/omlx.log` since 2026-09-10). omlx correctly excludes the incomplete model from `/v1/models` and 404s any chat request naming it. `internal/lifecycle/omlx.go`'s `start()` calls `e.warmup(ctx, origin+"/v1/chat/completions", path.Base(t.ModelName), health, e.warmupTimeout)`, and `warmup` (in `probe.go`) polls `tryChat` once per `pollInterval` (1s) for up to `warmupTimeout` (600s = 10 minutes), discarding every failure's actual reason. The result: `wt smoke` (and `wt start`) just sit there for up to 10 minutes with no visible cause before the generic `"failed to warm up model %s at %s"` error — which is what reads as "stuck." This fix does not repair the underlying incomplete download (that is local machine state, not a code bug — see the operational note at the end of this task); it makes the *next* time this happens (any model, any provider using `warmup`) immediately diagnosable instead of requiring a raw-log spelunk like this investigation needed.

- [x] **Step 1: Write the failing test**

Add to `wt/internal/lifecycle/probe_test.go`, after `TestWarmupRejectsNon2xx`:

```go
// TestWarmupTimeoutErrorIncludesLastFailureReason pins that a warmup
// timeout's error names the actual reason the last attempt failed, not just
// a generic "failed to warm up" message. Without this, a warmup loop that
// retries against a model the server will never serve (e.g. an incomplete
// download the server correctly excludes from /v1/models and 404s) looks
// indistinguishable from "still starting" for the entire warmupTimeout — a
// real incident (omlx, 2026-09-25) needed raw server-log spelunking to find
// the 404's explanation because this reason was being discarded here.
func TestWarmupTimeoutErrorIncludesLastFailureReason(t *testing.T) {
	e := testEnv()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Model 'm' not found. Available models: other-model"}`))
	}))
	defer srv.Close()

	err := e.warmup(context.Background(), srv.URL+"/v1/chat/completions", "m", srv.URL+"/health", e.warmupTimeout)
	if err == nil {
		t.Fatal("warmup: want an error, got nil")
	}
	for _, want := range []string{"404", "not found", "other-model"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("warmup err = %q, want it to contain %q (the server's own explanation)", err.Error(), want)
		}
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `cd wt && go test ./internal/lifecycle -run TestWarmupTimeoutErrorIncludesLastFailureReason -v`
Expected: FAIL — `warmup err = "failed to warm up model m at ..."`, missing "404"/"not found"/"other-model".

- [x] **Step 3: Change `tryChat` to return a reason alongside its bool**

In `wt/internal/lifecycle/probe.go`, add `"strings"` to the import block (alongside the existing `"regexp"`), then replace `tryChat`:

```go
func (e *env) tryChat(ctx context.Context, chatURL string, payload []byte) (ok bool, reason string) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, bytes.NewReader(payload))
	if err != nil {
		return false, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.chatClient.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, "reading response: " + err.Error()
	}
	// A non-2xx answer is not a warmup, even when its body echoes a
	// chat.completion marker: the ported probe raises HTTPError here and
	// retries. Accepting it reports a model as resident that is not.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncateForError(body))
	}
	if !chatCompletionMarker.Match(body) {
		return false, "response missing chat.completion marker: " + truncateForError(body)
	}
	return true, ""
}

// truncateForError bounds a server response body embedded in an error
// message: a warmup failure's body can be an HTML error page or a large
// JSON blob, and the caller's final error must stay a readable one-liner.
func truncateForError(b []byte) string {
	const max = 300
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
```

- [x] **Step 4: Update `warmup` to track and surface the last reason**

Replace the body of `warmup` in the same file:

```go
func (e *env) warmup(ctx context.Context, chatURL, model, healthURL string, timeout time.Duration) error {
	payload, _ := json.Marshal(map[string]any{
		"model":       model,
		"messages":    []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens":  1,
		"temperature": 0,
		"stream":      false,
	})
	deadline := time.Now().Add(timeout)
	var lastReason string
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if responded, _ := e.probe(ctx, healthURL, 2*time.Second); !responded {
			lastReason = "health check at " + healthURL + " did not respond"
			if err := e.sleep(ctx, e.pollInterval); err != nil {
				return err
			}
			continue
		}
		ok, reason := e.tryChat(ctx, chatURL, payload)
		if ok {
			return nil
		}
		lastReason = reason
		if err := e.sleep(ctx, e.pollInterval); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if lastReason == "" {
		lastReason = "no attempts completed"
	}
	return fmt.Errorf("failed to warm up model %s at %s: %s", model, chatURL, lastReason)
}
```

- [x] **Step 5: Run test to verify it passes**

Run: `cd wt && go test ./internal/lifecycle -run TestWarmupTimeoutErrorIncludesLastFailureReason -v`
Expected: PASS

- [x] **Step 6: Add a truncation test and a distinct-health-check-reason test, then run all three together**

A large response body (an HTML error page, a big JSON blob) must not balloon the final error, and a server that never answers the health check at all must report that distinctly from a chat-completion failure — otherwise a cold-starting server's timeout error could misleadingly repeat a stale reason from a different failure mode. Add both to `wt/internal/lifecycle/probe_test.go`:

```go
// TestTryChatReasonIsTruncated pins that a large response body embedded in
// tryChat's failure reason is bounded, not copied verbatim — a warmup
// failure against a server returning an HTML error page or a large JSON
// blob must not balloon the caller's final error message.
func TestTryChatReasonIsTruncated(t *testing.T) {
	e := testEnv()
	big := strings.Repeat("x", 10_000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(big))
	}))
	defer srv.Close()

	ok, reason := e.tryChat(context.Background(), srv.URL, []byte(`{}`))
	if ok {
		t.Fatal("tryChat: want ok=false for a 500 response")
	}
	if len(reason) > 400 {
		t.Errorf("reason length = %d, want it truncated well under the 10,000-byte body (max 300 chars of body plus a short prefix)", len(reason))
	}
}

// TestWarmupHealthCheckNeverRespondingReasonIsDistinct pins that a server
// whose health endpoint never answers reports that specifically, not a
// generic or stale chat-completion reason — a cold-starting server's
// eventual timeout error should say "did not respond", not misattribute the
// failure to whatever tryChat last returned in an earlier iteration.
func TestWarmupHealthCheckNeverRespondingReasonIsDistinct(t *testing.T) {
	e := testEnv()
	err := e.warmup(context.Background(), "http://127.0.0.1:1/chat", "m", "http://127.0.0.1:1/health", e.warmupTimeout)
	if err == nil {
		t.Fatal("warmup: want an error when the health endpoint never responds")
	}
	if !strings.Contains(err.Error(), "did not respond") {
		t.Errorf("warmup err = %q, want it to contain \"did not respond\"", err.Error())
	}
}
```

Run: `cd wt && go test ./internal/lifecycle -run 'TestWarmupTimeoutErrorIncludesLastFailureReason|TestTryChatReasonIsTruncated|TestWarmupHealthCheckNeverRespondingReasonIsDistinct' -v`
Expected: all three PASS.

- [x] **Step 7: Run the full lifecycle package's test suite to check for regressions**

Run: `cd wt && go test ./internal/lifecycle -v`
Expected: PASS (in particular `TestWarmupSendsOneTokenChatAndWaitsForCompletion` and `TestWarmupRejectsNon2xx`, which call `warmup` and check `err.Error()` for the substring `"warm"` — still present in the new message — and retry-count behavior, which the reason tracking does not change).

- [x] **Step 8: Build and vet the whole module**

Run: `cd wt && go build ./... && go vet ./...`
Expected: clean (no output, exit 0) — confirms no other file in the module called `tryChat` with the old single-return signature.

- [x] **Step 9: Commit**

```bash
cd wt
git add internal/lifecycle/probe.go internal/lifecycle/probe_test.go
git commit -m "$(cat <<'EOF'
fix(wt): warmup timeout errors name the last failure reason

The warmup poll loop discarded every failed attempt's HTTP status and body,
so a model that will never come up (e.g. an incomplete download the server
correctly excludes and 404s) looked identical to "still starting" for the
full warmupTimeout (600s) before a generic "failed to warm up" error. Root
cause found debugging omlx: took raw server-log spelunking (/opt/homebrew/
var/log/omlx.log) to find the informative 404 this loop was throwing away.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

**Operational note (not a code step — do separately, on this machine only):** the omlx warmup that started this investigation was failing against `omlx/mlx-community--Qwen3.8-27B-8bit`, whose on-disk download (`~/.omlx/models/Qwen3.8-27B-8bit`) is missing 6 of its weight shards. This plan's fix makes the *next* failure diagnosable in seconds instead of requiring a log investigation — it does not complete the download. Either re-run the download to resume it (omlx's own suggested fix, per its `model_discovery` warning) or remove/replace that registry entry if the model is no longer wanted.

---

### Task 3: `gpt-oss:20b` (and future `ollama_chat` deployments) drop the params that crash ollama's sampler

**Files:**
- Modify: `wt/internal/litellm/configfile.go`
- Test: `wt/internal/litellm/configfile_test.go`

**Interfaces:**
- Consumes: `toNode(v any) (n *yaml.Node, err error)` (existing, `internal/litellm/entry.go:44`), `mapGet`/`mapSet` (existing, same file).
- Produces: `droppedOllamaChatParams []string` (new package var) and `stringSeq(values []string) *yaml.Node` (new helper) — both used only within `configfile.go`.

**Root cause (from the investigation, isolated via direct curl tests against LiteLLM):**

| Request | Result |
|---|---|
| `gpt-oss:20b` via LiteLLM, no `tools`, no penalty params | works |
| `gpt-oss:20b` via LiteLLM, `frequency_penalty:0`/`presence_penalty:0`, no tools | `500`: `litellm.APIConnectionError: Ollama_chatException - {"error":{"code":400,"message":"Failed to initialize samplers: penalty_repeat must be finite and greater than 0"...}}` |
| `gpt-oss:20b` via LiteLLM, same penalty params **plus** a full `tools`/`parallel_tool_calls` payload (mimicking copilot) | same `500` |
| `qwen3.8:27b-mlx` via LiteLLM, same penalty params | works |
| `gpt-oss:20b` straight to ollama's own OpenAI-compat endpoint, same penalty params | works |

This is **not** a tool-calling capability gap — removing `tools` entirely still fails, and the identical params work fine against a different model and against ollama directly. It is specific to LiteLLM's `ollama_chat/*` bridge translating `frequency_penalty`/`presence_penalty` into ollama's native `repeat_penalty` option for this model. Copilot CLI always sends both params (at `0`), so every copilot × `gpt-oss:20b` request hits this. `internal/litellm/configfile.go`'s `EnsureSettings` already has a precedent fix for the same *shape* of bug: every `ollama_chat/*` row automatically gets `additional_drop_params: ["reasoning_effort"]` (added for the codex/`reasoning_effort` crash documented in `wt/docs/wt-agents/litellm-troubleshooting.md`). This task widens that same default list; it does not add new machinery.

**Constraint to respect:** `additional_drop_params` is presence-based (`mapGet(params, "additional_drop_params") == nil` guards the whole case) — a row that already has *any* list, including the currently-deployed `gpt-oss:20b` row's `[reasoning_effort]`, is left untouched by `EnsureSettings` on every future write. This is intentional (`TestSetRowPreservesUserManagedParams`) so a user's own opt-out (including a deliberate empty list) is never silently reset. That means this code change only affects **new** rows going forward (a fresh `wt litellm expose`); the currently-deployed `gpt-oss:20b` row needs a one-time manual edit, called out as a separate step below.

- [x] **Step 1: Write the failing test**

Add to `wt/internal/litellm/configfile_test.go`, after `TestEnsureSettings`:

```go
// TestEnsureSettingsDropsFrequencyAndPresencePenaltyOnFreshOllamaChatRows
// pins that a fresh (no additional_drop_params yet) ollama_chat/ row gets
// frequency_penalty and presence_penalty dropped alongside reasoning_effort.
// Root cause (2026-09-25, isolated via direct curl against LiteLLM): LiteLLM's
// ollama_chat bridge translates frequency_penalty/presence_penalty into
// ollama's native repeat_penalty, and for some models (observed with
// gpt-oss:20b) that translation can produce a value ollama's sampler rejects
// ("penalty_repeat must be finite and greater than 0") even when the caller
// sent 0 — copilot CLI always sends both params, so every copilot request
// against an affected model 500s. Without this, a fresh expose of any such
// model silently reintroduces the crash.
func TestEnsureSettingsDropsFrequencyAndPresencePenaltyOnFreshOllamaChatRows(t *testing.T) {
	f, _ := Open(writeConfig(t, `model_list:
  - model_name: ollama/gpt-oss:20b
    litellm_params: {model: ollama_chat/gpt-oss:20b}
`))
	f.EnsureSettings()
	out := enc(t, f)
	for _, want := range []string{"reasoning_effort", "frequency_penalty", "presence_penalty"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in additional_drop_params:\n%s", want, out)
		}
	}
}

// TestEnsureSettingsNeverExtendsAnExistingDropList pins the presence-based
// contract this task must not weaken: a row that already has
// additional_drop_params (even a single, now-incomplete entry like the
// pre-existing [reasoning_effort] this fix's own currently-deployed
// gpt-oss:20b row carries) is left exactly as-is by EnsureSettings — never
// silently widened. TestSetRowPreservesUserManagedParams already pins the
// general presence-based rule; this test pins it specifically for the case
// this task introduces (a list that predates the wider default and would
// otherwise look like an obvious "just add the missing ones" target).
func TestEnsureSettingsNeverExtendsAnExistingDropList(t *testing.T) {
	f, _ := Open(writeConfig(t, `model_list:
  - model_name: ollama/gpt-oss:20b
    litellm_params:
      model: ollama_chat/gpt-oss:20b
      additional_drop_params: [reasoning_effort]
`))
	f.EnsureSettings()
	out := enc(t, f)
	if strings.Contains(out, "frequency_penalty") || strings.Contains(out, "presence_penalty") {
		t.Fatalf("EnsureSettings widened an existing additional_drop_params list:\n%s", out)
	}
	if !strings.Contains(out, "reasoning_effort") {
		t.Fatalf("existing additional_drop_params entry lost:\n%s", out)
	}
}
```

- [x] **Step 2: Run tests to verify the first one fails**

Run: `cd wt && go test ./internal/litellm -run TestEnsureSettingsDropsFrequencyAndPresencePenaltyOnFreshOllamaChatRows -v`
Expected: FAIL — output is missing `frequency_penalty` and `presence_penalty`.

Run: `cd wt && go test ./internal/litellm -run TestEnsureSettingsNeverExtendsAnExistingDropList -v`
Expected: PASS already (current code never extends existing lists) — this one is a regression pin, not a red/green step; confirm it passes before and after Step 3.

- [x] **Step 3: Widen the default drop list**

In `wt/internal/litellm/configfile.go`, add a package var near `preservedParamKeys` (around line 58):

```go
// droppedOllamaChatParams are dropped by default on every fresh ollama_chat/
// deployment via additional_drop_params: reasoning_effort crashes litellm's
// ollama_chat responses bridge for codex (see
// docs/wt-agents/litellm-troubleshooting.md); frequency_penalty and
// presence_penalty map to ollama's repeat_penalty and can produce a value
// ollama's sampler rejects ("must be finite and greater than 0") for some
// models even when the caller sends 0 — copilot CLI always sends both.
var droppedOllamaChatParams = []string{"reasoning_effort", "frequency_penalty", "presence_penalty"}
```

Then add a helper next to `boolNode` (around line 154):

```go
// stringSeq builds a YAML sequence of string scalars. Each value is a
// literal Go string, so toNode always succeeds.
func stringSeq(values []string) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, v := range values {
		n, _ := toNode(v)
		seq.Content = append(seq.Content, n)
	}
	return seq
}
```

Then replace the `ollama_chat/` case in `EnsureSettings` (around line 310-313):

```go
		case strings.HasPrefix(model.Value, "ollama_chat/") && mapGet(params, "additional_drop_params") == nil:
			mapSet(params, "additional_drop_params", stringSeq(droppedOllamaChatParams))
```

- [x] **Step 4: Run both tests to verify they pass**

Run: `cd wt && go test ./internal/litellm -run 'TestEnsureSettingsDropsFrequencyAndPresencePenaltyOnFreshOllamaChatRows|TestEnsureSettingsNeverExtendsAnExistingDropList' -v`
Expected: both PASS.

- [x] **Step 5: Run the full litellm package's test suite to check for regressions**

Run: `cd wt && go test ./internal/litellm -v`
Expected: PASS, including `TestEnsureSettings` (still asserts `"reasoning_effort"` is present — unaffected by the widened list) and `TestSetRowPreservesUserManagedParams`.

- [x] **Step 6: Build and vet the whole module**

Run: `cd wt && go build ./... && go vet ./...`
Expected: clean.

- [x] **Step 7: Commit**

```bash
cd wt
git add internal/litellm/configfile.go internal/litellm/configfile_test.go
git commit -m "$(cat <<'EOF'
fix(wt): drop frequency_penalty/presence_penalty on fresh ollama_chat rows

LiteLLM's ollama_chat bridge maps frequency_penalty/presence_penalty to
ollama's native repeat_penalty; for gpt-oss:20b that translation produces a
value ollama's sampler rejects ("must be finite and greater than 0") even
when the caller sends 0. Copilot CLI always sends both params, so every
copilot request against gpt-oss:20b 500s. Isolated via direct curl against
LiteLLM (works with no tools/penalty params, works for qwen3.8:27b-mlx with
the same params, works straight to ollama with the same params — narrows to
this one bridge+model combination). Widens the existing
additional_drop_params default (already dropping reasoning_effort for the
codex bridge crash) the same presence-based way; a row with an existing list
is left untouched, matching TestSetRowPreservesUserManagedParams.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

**Operational note (not a code step — do separately, on this machine only):** the code change only affects rows written *fresh* from here on (e.g. a `wt litellm unexpose ollama/gpt-oss:20b && wt litellm expose ollama/gpt-oss:20b`, or any future first-time expose of an `ollama_chat` model). The currently-deployed `~/.config/litellm/config.yaml` row for `ollama/gpt-oss:20b` already has `additional_drop_params: [reasoning_effort]` and will **not** be touched automatically (presence-based, by design — see Task 3's constraint above). To fix the live proxy immediately: hand-edit that row's `additional_drop_params` to `[reasoning_effort, frequency_penalty, presence_penalty]`, then restart the proxy (`wt litellm sync` or the `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy` fallback), then verify with the same direct curl this investigation used:

```bash
KEY=$(grep -A5 '\[litellm\]' ~/.config/agent-wt/config.toml | grep api_key | sed 's/.*"\(.*\)"/\1/')
curl -s http://localhost:4000/v1/chat/completions -H "Content-Type: application/json" -H "Authorization: Bearer $KEY" \
  -d '{"model":"ollama/gpt-oss:20b","messages":[{"role":"user","content":"hi"}],"max_tokens":5,"frequency_penalty":0,"presence_penalty":0}'
# expect a normal chat.completion response, not a 500
```

---

## Final verification (all three tasks)

- [x] **Run the whole module's test suite**

Run: `cd wt && go build ./... && go vet ./... && go test ./...`
Expected: PASS, no vet warnings.

- [x] **Run `make check`** (shellcheck + shfmt + go-format-check) from `wt/`
Expected: clean.

- [x] **Live re-verification** (this machine, after `make install` from `wt/`):

```bash
wt smoke ollama/gpt-oss:20b --only copilot --timeout 60s   # was 500, should now PASS
wt smoke mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality --only copilot --timeout 120s
                                                             # should fail fast/cleanly if it fails at all,
                                                             # never spiral into a multi-minute permission-denial loop
```

(The omlx incomplete-download row will still fail warmup until the operational note in Task 2 is followed — verify its *error message* now names the real reason instead of just timing out silently.)
