package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/claudelogin"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/smoke"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// subAliasRow is an alias whose one target is on the Claude subscription.
func subAliasRow() modelapi.Row {
	return modelapi.Row{ID: "alias/sonnet", GatewayID: "alias/sonnet", ProviderID: "alias", ModelName: "sonnet", Kind: modelapi.KindAlias,
		Location: "cloud", Tags: []string{"code"}, Registered: true, State: modelapi.StateRunning, Action: modelapi.ActionLaunch, Order: 8,
		Targets: []modelapi.AliasTarget{{ID: "claude-sonnet-5-5", Order: 1, ProviderID: config.SubscriptionProvider, Location: "cloud", State: modelapi.StateRunning}}}
}

// recordingClaude puts a fake claude first on PATH that appends one line per
// invocation (its argv) to a log, and saves the environment of its last
// invocation. It returns the log's path and the env dump's. The real claude
// is never run.
func recordingClaude(t *testing.T) (log, envDump string) {
	t.Helper()
	dir := t.TempDir()
	log, envDump = filepath.Join(dir, "invocations.log"), filepath.Join(dir, "env.txt")
	script := "#!/bin/sh\necho \"ARGS: $*\" >> " + log + "\nenv > " + envDump + "\nexit 0\n"
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log, envDump
}

// invocations is the fake claude's log, one entry per run.
func invocations(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// stubSmokePicker makes the smoke picker choose id, recording what it was
// offered.
func stubSmokePicker(t *testing.T, id string, offered *[]string) {
	t.Helper()
	old := pickModelTUI
	t.Cleanup(func() { pickModelTUI = old })
	pickModelTUI = func(models []config.Model, _ themes.Theme) (config.Model, bool, error) {
		for _, m := range models {
			*offered = append(*offered, m.ID)
		}
		for _, m := range models {
			if m.ID == id {
				return m, true, nil
			}
		}
		return config.Model{}, false, nil
	}
}

// TestSmokeNeverSpendsTheSubscriptionByDefault is decision 9 (#597): a smoke
// run without --subscription never runs claude on a subscription model or on
// an alias with a subscription target, even on a machine that opted in with a
// login and a server whose passthrough is on. On a terminal (text and --json)
// the picker never offers them and each is one SKIP row (agent claude, the
// hint in error); with no terminal the run is refused before anything. It
// matters because a smoke row is a real request: by default it must not
// spend the owner's plan.
func TestSmokeNeverSpendsTheSubscriptionByDefault(t *testing.T) {
	for _, mode := range []string{"text", "json"} {
		t.Run(mode, func(t *testing.T) {
			st := goodLogin()
			r, calls := subRig(t, true, true, &st, wireRow("openrouter/a", modelapi.ActionLaunch, 1), subRow("claude-haiku-5-5"), subAliasRow())
			log, _ := recordingClaude(t)
			stubStdinTTY(t, true)
			var offered []string
			stubSmokePicker(t, "openrouter/a", &offered)
			args := []string{"--only", "claude", "--prompt", "hi"}
			if mode == "json" {
				args = append(args, "--json")
			}
			run := smokeOnFake(t, r, args...)
			if run.err != nil || len(run.exits) != 0 {
				t.Fatalf("err = %v exits = %v\nstderr: %s", run.err, run.exits, run.stderr)
			}
			for _, id := range offered {
				if id == "claude-haiku-5-5" || id == "alias/sonnet" {
					t.Errorf("the picker offered %s", id)
				}
			}
			inv := invocations(t, log)
			if len(inv) != 1 || !strings.Contains(inv[0], "openrouter/a") {
				t.Errorf("claude invocations = %q, want exactly one, for openrouter/a", inv)
			}
			for _, line := range inv {
				if strings.Contains(line, "claude-haiku-5-5") || strings.Contains(line, "alias/sonnet") || strings.Contains(line, "claude-sonnet-5-5") {
					t.Errorf("claude ran on a subscription model: %s", line)
				}
			}
			if *calls != 0 {
				t.Errorf("the login check ran %d times, want 0", *calls)
			}
			for _, reg := range r.f.Registers {
				for _, id := range reg {
					if id != "openrouter/a" {
						t.Errorf("registered %s", id)
					}
				}
			}
			if mode == "json" {
				var rep smokeJSONReport
				if err := json.Unmarshal([]byte(run.stdout), &rep); err != nil {
					t.Fatalf("stdout is not the --json report: %v\n%s", err, run.stdout)
				}
				skips := map[string]int{}
				for _, row := range rep.Rows {
					if row.Status == string(smoke.StatusSkip) {
						if row.Agent != "claude" || row.Error != smoke.SubscriptionSkipReason {
							t.Errorf("SKIP row = %+v, want agent claude and the hint", row)
						}
						skips[row.Model]++
					}
				}
				if skips["claude-haiku-5-5"] != 1 || skips["alias/sonnet"] != 1 || len(skips) != 2 {
					t.Errorf("SKIP rows per model = %v, want one each for claude-haiku-5-5 and alias/sonnet", skips)
				}
			} else {
				for _, id := range []string{"claude-haiku-5-5", "alias/sonnet"} {
					if n := strings.Count(run.stdout, "[SKIP ] claude     "+id+" "); n != 1 {
						t.Errorf("SKIP lines for %s = %d, want 1\n%s", id, n, run.stdout)
					}
					if !strings.Contains(run.stderr, "claude x "+id+" - SKIP") || !strings.Contains(run.stderr, smoke.SubscriptionSkipReason) {
						t.Errorf("stderr lacks the SKIP progress line with the hint for %s:\n%s", id, run.stderr)
					}
				}
				if !strings.Contains(run.stdout, "SKIP: 2") {
					t.Errorf("counts line lacks SKIP: 2:\n%s", run.stdout)
				}
			}
			if strings.Contains(run.stdout+run.stderr, subTok) {
				t.Error("smoke output carries the wt token")
			}
		})
	}
	t.Run("no terminal", func(t *testing.T) {
		st := goodLogin()
		r, calls := subRig(t, true, true, &st, subRow("claude-haiku-5-5"), subAliasRow())
		log, _ := recordingClaude(t)
		for _, args := range [][]string{{}, {"--json"}} {
			run := smokeOnFake(t, r, args...)
			if run.err == nil || !strings.Contains(run.err.Error(), "needs a TTY") {
				t.Errorf("%v: err = %v, want the TTY refusal", args, run.err)
			}
		}
		if inv := invocations(t, log); len(inv) != 0 || *calls != 0 {
			t.Errorf("claude ran %q, login checks %d; want neither", inv, *calls)
		}
		nothingRegistered(t, r.f)
	})
}

// TestSmokeNamedSubscriptionNeedsTheFlag proves a subscription model or an
// alias with a subscription target named without --subscription is exit 1
// with the hint, before any login check, register or agent run (decision 9):
// naming the id is not consent to spend the plan.
func TestSmokeNamedSubscriptionNeedsTheFlag(t *testing.T) {
	for _, id := range []string{"claude-haiku-5-5", "alias/sonnet"} {
		for _, extra := range [][]string{nil, {"--json"}} {
			st := goodLogin()
			r, calls := subRig(t, true, true, &st, subRow("claude-haiku-5-5"), subAliasRow())
			log, _ := recordingClaude(t)
			run := smokeOnFake(t, r, append([]string{id}, extra...)...)
			if run.err == nil || exitCodeOf(run.err) > 1 || run.err.Error() != id+" "+smoke.SubscriptionSkipReason {
				t.Fatalf("%s %v: err = %v, want %q", id, extra, run.err, id+" "+smoke.SubscriptionSkipReason)
			}
			if inv := invocations(t, log); len(inv) != 0 || *calls != 0 {
				t.Errorf("%s: claude ran %q, login checks %d; want neither", id, inv, *calls)
			}
			nothingRegistered(t, r.f)
		}
	}
}

// TestSmokeSubscriptionNeedsTheID proves --subscription names one
// subscription model or alias: without an id, or with an id that is not on
// the subscription (or unknown), it is exit 1 with one message and nothing
// runs. A flag that widened a whole walk would spend the plan on every
// subscription row at once.
func TestSmokeSubscriptionNeedsTheID(t *testing.T) {
	const msg = "--subscription needs the id of one subscription model or alias"
	for _, args := range [][]string{{"--subscription"}, {"--subscription", "--json"}, {"openrouter/a", "--subscription"}, {"nope/x", "--subscription"}} {
		st := goodLogin()
		r, calls := subRig(t, true, true, &st, wireRow("openrouter/a", modelapi.ActionLaunch, 1), subRow("claude-haiku-5-5"))
		stubStdinTTY(t, true)
		log, _ := recordingClaude(t)
		run := smokeOnFake(t, r, args...)
		if run.err == nil || run.err.Error() != msg || exitCodeOf(run.err) > 1 {
			t.Fatalf("%v: err = %v, want %q", args, run.err, msg)
		}
		if inv := invocations(t, log); len(inv) != 0 || *calls != 0 {
			t.Errorf("%v: claude ran %q, login checks %d; want neither", args, inv, *calls)
		}
		nothingRegistered(t, r.f)
	}
}

// TestSmokeSubscriptionRunsOnlyClaude proves `wt smoke <id> --subscription`
// runs the one row it names with claude alone, in header mode: the token
// travels only in ANTHROPIC_CUSTOM_HEADERS (an inherited x-wt-model-token
// line is replaced, other lines kept), no ANTHROPIC_AUTH_TOKEN, and the
// guard runs before the client session's register. --only naming another
// agent is refused. Smoke output never carries the token.
func TestSmokeSubscriptionRunsOnlyClaude(t *testing.T) {
	st := goodLogin()
	r, calls := subRig(t, true, true, &st, wireRow("openrouter/a", modelapi.ActionLaunch, 1), subRow("claude-haiku-5-5"), subAliasRow())
	log, envDump := recordingClaude(t)
	t.Setenv("ANTHROPIC_CUSTOM_HEADERS", "x-wt-model-token: stale-SENTINEL\nx-litellm-tags: keep")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "inherited-FAKE")
	run := smokeOnFake(t, r, "claude-haiku-5-5", "--subscription", "--prompt", "hi")
	if run.err != nil || len(run.exits) != 0 {
		t.Fatalf("err = %v exits = %v\nstderr: %s", run.err, run.exits, run.stderr)
	}
	inv := invocations(t, log)
	if len(inv) != 1 || !strings.Contains(inv[0], "claude-haiku-5-5") {
		t.Fatalf("claude invocations = %q, want one for claude-haiku-5-5", inv)
	}
	if !strings.Contains(run.stdout, "[PASS ] claude     claude-haiku-5-5") || strings.Count(run.stdout, "[") != 1 {
		t.Errorf("stdout = %q, want one PASS row for claude only", run.stdout)
	}
	if *calls != 1 {
		t.Errorf("the login check ran %d times, want 1", *calls)
	}
	reqs := r.f.reqs()
	if g, reg := index(reqs, "GUARD"), index(reqs, "POST /control/v1/sessions"); g < 0 || reg < 0 || g > reg {
		t.Errorf("guard at %d, register at %d: want the guard first\n%s", g, reg, strings.Join(reqs, "\n"))
	}
	b, err := os.ReadFile(envDump)
	if err != nil {
		t.Fatal(err)
	}
	env := string(b)
	if strings.Contains(env, "stale-SENTINEL") || !strings.Contains(env, "x-wt-model-token: "+subTok) || !strings.Contains(env, "x-litellm-tags: keep") {
		t.Errorf("ANTHROPIC_CUSTOM_HEADERS not merged in header mode:\n%s", env)
	}
	if strings.Contains(env, "ANTHROPIC_AUTH_TOKEN=") {
		t.Errorf("header mode kept ANTHROPIC_AUTH_TOKEN:\n%s", env)
	}
	if strings.Contains(run.stdout+run.stderr, subTok) {
		t.Error("smoke output carries the wt token")
	}

	for _, only := range []string{"pi", "codex"} {
		r2, _ := subRig(t, true, true, &st, subRow("claude-haiku-5-5"))
		run := smokeOnFake(t, r2, "claude-haiku-5-5", "--subscription", "--only", only)
		if run.err == nil || !strings.Contains(run.err.Error(), "not eligible") {
			t.Errorf("--only %s: err = %v, want the not-eligible refusal", only, run.err)
		}
		nothingRegistered(t, r2.f)
	}
}

// TestSmokeSubscriptionRunsTheGuard proves `wt smoke <id> --subscription`
// runs Task 6's guard before any client session or agent run: no claude.ai
// login, or a machine that has not opted in, is exit 1 with the guard's
// refusal, nothing registered and claude never run.
func TestSmokeSubscriptionRunsTheGuard(t *testing.T) {
	noLogin := claudelogin.Status{LoggedIn: false, Method: "none", Provider: "firstParty", Version: "2.1.330"}
	good := goodLogin()
	for _, tc := range []struct {
		name  string
		optIn bool
		st    *claudelogin.Status
		want  string
	}{
		{"no login", true, &noLogin, "no claude.ai login was found"},
		{"not opted in", false, &good, "has not opted in"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := subRig(t, tc.optIn, true, tc.st, subRow("claude-haiku-5-5"))
			log, _ := recordingClaude(t)
			run := smokeOnFake(t, r, "claude-haiku-5-5", "--subscription", "--prompt", "hi")
			if run.err == nil || exitCodeOf(run.err) > 1 || !strings.Contains(run.err.Error(), tc.want) {
				t.Fatalf("err = %v, want exit 1 with %q", run.err, tc.want)
			}
			if inv := invocations(t, log); len(inv) != 0 {
				t.Errorf("claude ran: %q", inv)
			}
			nothingRegistered(t, r.f)
		})
	}
}
