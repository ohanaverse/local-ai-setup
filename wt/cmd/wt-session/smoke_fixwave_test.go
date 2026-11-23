package main

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/smoke"
)

// mixedAliasRow is an alias whose first target is an ordinary cloud model and
// whose second is on the Claude subscription.
func mixedAliasRow() modelapi.Row {
	return modelapi.Row{ID: "alias/mixed", GatewayID: "alias/mixed", ProviderID: "alias", ModelName: "mixed", Kind: modelapi.KindAlias,
		Location: "cloud", Tags: []string{"code"}, Registered: true, State: modelapi.StateRunning, Action: modelapi.ActionLaunch, Order: 7,
		Targets: []modelapi.AliasTarget{
			{ID: "openrouter/a", Order: 1, ProviderID: "openrouter", Location: "cloud", State: modelapi.StateRunning},
			{ID: "claude-sonnet-5-5", Order: 2, ProviderID: config.SubscriptionProvider, Location: "cloud", State: modelapi.StateRunning},
		}}
}

// TestSmokeProfileApplierHandsOnTheHeaderMode pins the smoke applier's
// backstop (#597, decision 7) with the guard's profile-key step out of the
// way: called the way a header-mode row calls it, a launch profile that sets
// ANTHROPIC_CUSTOM_HEADERS is refused (naming the key, never the value) and
// the command is left as it was; called in token mode the same profile
// applies. A smoke applier that dropped the build's header mode (always
// false) would apply the header-overriding profile to a header-mode row.
func TestSmokeProfileApplierHandsOnTheHeaderMode(t *testing.T) {
	rp := profiles.ResolvedProfile{Env: map[string]string{"ANTHROPIC_CUSTOM_HEADERS": "x-wt-model-token: V-SENTINEL"}}
	pa := smokeProfileApplier("claude", rp)
	cmd := exec.Command("true")
	cmd.Env = []string{"A=1"}
	_, err := pa(cmd, nil, true)
	if err == nil || !strings.Contains(err.Error(), "ANTHROPIC_CUSTOM_HEADERS") || strings.Contains(err.Error(), "V-SENTINEL") {
		t.Fatalf("header mode: err = %v, want the refusal naming the key and no value", err)
	}
	if !slices.Equal(cmd.Env, []string{"A=1"}) {
		t.Errorf("a refused profile changed the command's env: %q", cmd.Env)
	}
	cleanup, err := pa(cmd, nil, false)
	if err != nil {
		t.Fatalf("token mode: err = %v, want the profile applied", err)
	}
	if cleanup != nil {
		_ = cleanup()
	}
	if !slices.ContainsFunc(cmd.Env, func(e string) bool { return strings.HasPrefix(e, "ANTHROPIC_CUSTOM_HEADERS=") }) {
		t.Errorf("token mode did not apply the profile: %q", cmd.Env)
	}
}

// TestSmokeOnlyOtherAgentPrintsNoSubscriptionSkip proves a picker run with
// --only naming another agent reports no SKIP row for the subscription models
// it left out: those rows are about claude, and --only asked about pi alone.
func TestSmokeOnlyOtherAgentPrintsNoSubscriptionSkip(t *testing.T) {
	st := goodLogin()
	r, calls := subRig(t, true, true, &st, wireRow("openrouter/a", modelapi.ActionLaunch, 1), subRow("claude-haiku-5-5"), subAliasRow())
	stubGatewayRows(t, r, passRows)
	stubStdinTTY(t, true)
	var offered []string
	stubSmokePicker(t, "openrouter/a", &offered)
	run := smokeOnFake(t, r, "--only", "pi", "--prompt", "hi")
	if run.err != nil || len(run.exits) != 0 {
		t.Fatalf("err = %v exits = %v\nstderr: %s", run.err, run.exits, run.stderr)
	}
	if strings.Contains(run.stdout, "[SKIP") || !strings.Contains(run.stdout, "SKIP: 0") || strings.Contains(run.stderr, smoke.SubscriptionSkipReason) {
		t.Errorf("--only pi printed a subscription SKIP row:\nstdout: %s\nstderr: %s", run.stdout, run.stderr)
	}
	if !strings.Contains(run.stdout, "[PASS ] pi") {
		t.Errorf("stdout = %q, want the pi row", run.stdout)
	}
	if *calls != 0 {
		t.Errorf("the login check ran %d times, want 0", *calls)
	}
}

// TestSmokeMixedAliasNeedsTheFlag proves an alias whose targets mix an
// ordinary model and a subscription model is a subscription alias for smoke
// (decision 9): a plain picker run never offers it and reports it as one
// SKIP row, and `wt smoke alias/mixed --subscription` runs it with claude
// alone, after the guard and before the client session's register.
func TestSmokeMixedAliasNeedsTheFlag(t *testing.T) {
	st := goodLogin()
	r, calls := subRig(t, true, true, &st, wireRow("openrouter/a", modelapi.ActionLaunch, 1), mixedAliasRow())
	log, _ := recordingClaude(t)
	stubStdinTTY(t, true)
	var offered []string
	stubSmokePicker(t, "openrouter/a", &offered)
	run := smokeOnFake(t, r, "--only", "claude", "--prompt", "hi")
	if run.err != nil || len(run.exits) != 0 {
		t.Fatalf("plain: err = %v exits = %v\nstderr: %s", run.err, run.exits, run.stderr)
	}
	if slices.Contains(offered, "alias/mixed") {
		t.Errorf("the picker offered alias/mixed: %q", offered)
	}
	if n := strings.Count(run.stdout, "[SKIP ] claude     alias/mixed "); n != 1 {
		t.Errorf("SKIP rows for alias/mixed = %d, want 1\n%s", n, run.stdout)
	}
	for _, line := range invocations(t, log) {
		if strings.Contains(line, "alias/mixed") {
			t.Errorf("a plain run ran claude on alias/mixed: %s", line)
		}
	}
	if *calls != 0 {
		t.Errorf("plain: the login check ran %d times, want 0", *calls)
	}

	r2, calls2 := subRig(t, true, true, &st, wireRow("openrouter/a", modelapi.ActionLaunch, 1), mixedAliasRow())
	log2, _ := recordingClaude(t)
	run = smokeOnFake(t, r2, "alias/mixed", "--subscription", "--prompt", "hi")
	if run.err != nil || len(run.exits) != 0 {
		t.Fatalf("--subscription: err = %v exits = %v\nstderr: %s", run.err, run.exits, run.stderr)
	}
	inv := invocations(t, log2)
	if len(inv) != 1 || !strings.Contains(inv[0], "alias/mixed") {
		t.Errorf("claude invocations = %q, want one for alias/mixed", inv)
	}
	if !strings.Contains(run.stdout, "[PASS ] claude     alias/mixed") || strings.Count(run.stdout, "[") != 1 {
		t.Errorf("stdout = %q, want one PASS row for claude only", run.stdout)
	}
	if *calls2 != 1 {
		t.Errorf("--subscription: the login check ran %d times, want 1", *calls2)
	}
	reqs := r2.f.reqs()
	if g, reg := index(reqs, "GUARD"), index(reqs, "POST /control/v1/sessions"); g < 0 || reg < 0 || g > reg {
		t.Errorf("guard at %d, register at %d: want the guard first\n%s", g, reg, strings.Join(reqs, "\n"))
	}
}

// TestSmokeAllSubscriptionWalkGrammar pins the all-subscription walk's
// refusal (exit 1, nothing run): one id reads "<id> uses ...", several read
// "<a>, <b> use ...".
func TestSmokeAllSubscriptionWalkGrammar(t *testing.T) {
	for _, tc := range []struct {
		rows []modelapi.Row
		want string
	}{
		{[]modelapi.Row{subRow("claude-haiku-5-5")},
			"the model server has no models to test without --subscription: claude-haiku-5-5 uses your Claude subscription: pass --subscription to smoke it"},
		{[]modelapi.Row{subRow("claude-haiku-5-5"), subAliasRow()},
			"the model server has no models to test without --subscription: alias/sonnet, claude-haiku-5-5 use your Claude subscription: pass --subscription to smoke it"},
	} {
		st := goodLogin()
		r, calls := subRig(t, true, true, &st, tc.rows...)
		log, _ := recordingClaude(t)
		stubStdinTTY(t, true)
		run := smokeOnFake(t, r, "--prompt", "hi")
		if run.err == nil || run.err.Error() != tc.want || exitCodeOf(run.err) > 1 {
			t.Errorf("err = %v, want %q", run.err, tc.want)
		}
		if inv := invocations(t, log); len(inv) != 0 || *calls != 0 {
			t.Errorf("claude ran %q, login checks %d; want neither", inv, *calls)
		}
		nothingRegistered(t, r.f)
	}
}
