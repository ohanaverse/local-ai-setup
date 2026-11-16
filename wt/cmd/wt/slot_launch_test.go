package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
	"github.com/spf13/cobra"
)

// startCall is what one engine call of a start carried.
type startCall struct{ replace, owed bool }

// slotFixtureConfig is a config with agent "claude" (providers "openrouter",
// "omlx"), cloud models "openrouter/a" and "openrouter/b", local model
// "omlx/q" (model_name "q"), and [litellm] url "http://localhost:4000",
// key "sk-test-1234", enabled false. Under TestMain's inventory stub
// (localmodels.OnDiskSnapshotForTest) "omlx/q" is on disk and not running:
// a start row.
func slotFixtureConfig() *config.Config {
	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "openrouter", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "api_key"}},
			{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}},
		},
		Models: []config.Model{
			{ID: "openrouter/a", ProviderID: "openrouter", ModelName: "a", Family: "a", Tags: []string{"code"}},
			{ID: "openrouter/b", ProviderID: "openrouter", ModelName: "b", Family: "b", Tags: []string{"code"}},
			{ID: "omlx/q", ProviderID: "omlx", ModelName: "q", Family: "q", Tags: []string{"code"}},
		},
		Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"openrouter", "omlx"}}},
	}
	cfg.SetLitellmForTest(config.LitellmState{URL: "http://localhost:4000", APIKey: "sk-test-1234"})
	return cfg
}

// slotFixtureCheck is a Usable check "profile/<name>" for agent "claude",
// with claude's four slots declared and no static error.
func slotFixtureCheck(index int, name, def string, slots map[string]string) profiles.SlotCheck {
	return profiles.SlotCheck{
		Index:    index,
		Profile:  profiles.SlotProfile{Name: name, Agent: "claude", Default: def, Slots: slots},
		Usable:   true,
		Declared: []string{"fable", "opus", "sonnet", "haiku"},
	}
}

// slotCloud and slotMixed are the two slot maps most tests use. The default
// slot is "opus" in both, so the distinct models come out in the order
// written here: openrouter/a first.
func slotCloud() map[string]string {
	return map[string]string{"opus": "openrouter/a", "haiku": "openrouter/b"}
}

func slotMixed() map[string]string {
	return map[string]string{"opus": "openrouter/a", "haiku": "omlx/q"}
}

// slotLaunchEnv gives one test its own config home and worktree. It returns
// wt's state directory inside that home (where usage.jsonl, rotation.state,
// last-picked.state would be written) and the worktree.
func slotLaunchEnv(t *testing.T) (stateDir, worktree string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	return filepath.Join(home, "agent-wt"), t.TempDir()
}

// slotRow resolves check against the model server's rows of slotWireRows
// (openrouter/a and openrouter/b are cloud launch rows), which is enough for
// an all-cloud profile.
func slotRow(check profiles.SlotCheck) catalog.Row {
	return catalog.ResolveSlotProfileWire(catalog.WireByID(slotWireRows()), check)
}

// TestApplySlotProfileAppliesArgsAndWrapper verifies the applier applies the
// two mechanisms the slot build does not deliver: the profile's args are
// appended to the command, and its wrapper then wraps the whole command line.
// Claude allows neither today; an agent that does must not lose them.
func TestApplySlotProfileAppliesArgsAndWrapper(t *testing.T) {
	_, worktree := slotLaunchEnv(t)
	fakeAgentOnPath(t, "wt-test-wrapper")
	check := slotFixtureCheck(0, "cloud", "opus", slotCloud())
	check.Profile.Args = []string{"--effort", "high"}
	check.Profile.Wrapper = &profiles.WrapperSpec{Binary: "wt-test-wrapper", ArgsTemplate: []string{"run", "{{args}}"}}
	check.Profile.Env = map[string]string{"FROM_PROFILE": "1"}
	check.Profile.ConfigContent = map[string]any{"x": "1"}
	row := slotRow(check)
	cmd := exec.Command("true", "--model", "opus")
	cmd.Dir = worktree

	cleanup, err := applySlotProfile(cmd, "claude", row)
	if err != nil {
		t.Fatalf("applySlotProfile() error = %v", err)
	}
	if cleanup == nil || cleanup() != nil {
		t.Error("cleanup must be a non-nil no-op")
	}
	if got, want := strings.Join(cmd.Args[1:], " "), "run --model opus --effort high"; got != want {
		t.Errorf("args = %q, want %q", got, want)
	}
	if filepath.Base(cmd.Path) != "wt-test-wrapper" {
		t.Errorf("cmd.Path = %q, want the wrapper binary", cmd.Path)
	}
	// env and config_content are the slot build's to deliver, not the
	// applier's: applied here too they would be set twice, once as a file.
	if slices.Contains(cmd.Env, "FROM_PROFILE=1") {
		t.Errorf("cmd.Env = %v: the applier must not apply the profile's env", cmd.Env)
	}
	if _, statErr := os.Stat(filepath.Join(worktree, ".claude")); !os.IsNotExist(statErr) {
		t.Error("the applier wrote under .claude in the worktree")
	}
}

// TestApplySlotProfileFailsClosed verifies an apply error is returned, not
// swallowed. The automatic-profile applier degrades to an unprofiled launch
// on a config error; for a slot profile that would start Claude Code without
// what the profile asked for, so the launch must stop.
func TestApplySlotProfileFailsClosed(t *testing.T) {
	_, worktree := slotLaunchEnv(t)
	check := slotFixtureCheck(0, "cloud", "opus", slotCloud())
	check.Profile.Wrapper = &profiles.WrapperSpec{Binary: "wt-test-definitely-missing-binary-xyz", ArgsTemplate: []string{"{{args}}"}}
	cmd := exec.Command("true")
	cmd.Dir = worktree

	cleanup, err := applySlotProfile(cmd, "claude", slotRow(check))
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("err = %v, want the wrapper-not-installed error", err)
	}
	if cleanup == nil {
		t.Error("cleanup must not be nil, even on an error")
	}

	if _, err := applySlotProfile(exec.Command("true"), "claude", catalog.Row{Model: config.Model{ID: "openrouter/a"}}); err == nil {
		t.Error("a model row was accepted: the applier must refuse a row that is not a slot profile")
	}
}

// TestApplySlotProfileSelfHealsALeftoverSettingsFile verifies the self-heal
// of a settings.local.json left behind by an earlier automatic-profile
// session still runs before a profile launch. Without it a profile session
// would start with that stale file in the worktree, and its env block could
// redirect the session.
func TestApplySlotProfileSelfHealsALeftoverSettingsFile(t *testing.T) {
	_, worktree := slotLaunchEnv(t)
	target := filepath.Join(worktree, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(`{"hooks":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rp := profiles.ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"X": "1"}}}
	if _, err := profiles.ApplyConfigContent(exec.Command("claude"), "claude", worktree, rp); err != nil {
		t.Fatalf("seed an orphaned backup: %v", err)
	}
	cmd := exec.Command("true")
	cmd.Dir = worktree
	row := slotRow(slotFixtureCheck(0, "cloud", "opus", slotCloud()))

	var err error
	stderr := captureFile(t, &os.Stderr, func() {
		_, err = applySlotProfile(cmd, "claude", row)
	})
	if err != nil {
		t.Fatalf("applySlotProfile() error = %v", err)
	}
	if !strings.Contains(stderr, "wt: restored a leftover profile-managed file from a previous session: "+target) {
		t.Errorf("stderr = %q, want the self-heal notice", stderr)
	}
	// The restore rewrites the file's JSON, so compare what it holds, not
	// its bytes: the user's own key is back and the old profile's is gone.
	data, rerr := os.ReadFile(target)
	if rerr != nil || !strings.Contains(string(data), `"hooks"`) || strings.Contains(string(data), `"env"`) {
		t.Errorf("target = %q (%v), want the user's hooks kept and the old profile's env block removed", data, rerr)
	}
}

// TestSlotProfileWritesNoSettingsFile verifies a Claude profile with
// config_content, run through the real slot build and the applier, writes
// nothing: no .claude directory in the worktree, and nothing in the config
// home except the self-heal check's lock file. config_content travels in the
// inline --settings argument, so an interrupted session leaves no proxy-only
// ids behind for a later bare `claude` run. The gateway token stays out of
// argv.
func TestSlotProfileWritesNoSettingsFile(t *testing.T) {
	stateDir, worktree := slotLaunchEnv(t)
	fakeAgentOnPath(t, "claude")
	check := slotFixtureCheck(0, "cloud", "opus", slotCloud())
	check.Profile.Env = map[string]string{"MAX_THINKING_TOKENS": "4096"}
	check.Profile.ConfigContent = map[string]any{"modelOverrides": map[string]any{"claude-sonnet-4-6": "openrouter/b"}}
	row := slotRow(check)
	if row.Action() != catalog.ActionLaunch {
		t.Fatalf("fixture: the row is not a launch row: %s", row.BlockReason())
	}

	gw := agents.Gateway{Origin: "http://127.0.0.1:1", Token: "sk-test-1234"}
	cmd, err := agents.BuildSlotLaunchCmdGateway(row.Profile.Launch(), worktree, false, gw, nil)
	if err != nil {
		t.Fatalf("BuildSlotLaunchCmdGateway() error = %v", err)
	}
	cleanup, err := applySlotProfile(cmd, "claude", row)
	if err != nil {
		t.Fatalf("applySlotProfile() error = %v", err)
	}
	if cerr := cleanup(); cerr != nil {
		t.Fatalf("cleanup() error = %v", cerr)
	}

	i := slices.Index(cmd.Args, "--settings")
	if i < 0 || i+1 >= len(cmd.Args) || !strings.Contains(cmd.Args[i+1], "modelOverrides") {
		t.Errorf("args = %q, want config_content inside the --settings value", cmd.Args)
	}
	for _, a := range cmd.Args {
		if strings.Contains(a, "sk-test-1234") {
			t.Errorf("the gateway token is in argv: %q", a)
		}
	}
	if _, statErr := os.Stat(filepath.Join(worktree, ".claude")); !os.IsNotExist(statErr) {
		t.Error("a .claude directory exists in the worktree: a settings file was written")
	}
	entries, _ := os.ReadDir(worktree)
	if len(entries) != 0 {
		t.Errorf("the worktree holds %d entries, want none", len(entries))
	}
	// The one thing the applier may leave in the config home is the lock file
	// of the self-heal check (profile-backups/<key>.lock). A backup marker
	// there would mean a file was rewritten.
	_ = filepath.WalkDir(filepath.Dir(stateDir), func(path string, d os.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) == "profile-backups" && strings.HasSuffix(path, ".lock") {
			return nil
		}
		t.Errorf("the config home gained a file: %s", path)
		return nil
	})
}

// TestRunSlotAgentCmdPostExitFlow verifies what follows a profile session:
// the client session is ended (tail.End), then the summary line names the
// profile and the duration. There is no per-model price notice and no survey,
// since neither belongs to one model here, and no stop picker: the model
// server stops idle models after its grace.
func TestRunSlotAgentCmdPostExitFlow(t *testing.T) {
	slotLaunchEnv(t)
	var order []string
	tail := runTail{End: func() {
		order = append(order, "end")
		fmt.Fprint(os.Stdout, "END-MARKER\n")
	}}
	row := slotRow(slotFixtureCheck(0, "cloud", "opus", slotCloud()))
	cleaned := false

	var err error
	stdout := captureFile(t, &os.Stdout, func() {
		err = runSlotAgentCmd(exec.Command("true"), "claude", row, func() error { cleaned = true; return nil }, tail)
	})

	if err != nil {
		t.Fatalf("runSlotAgentCmd() error = %v", err)
	}
	if !cleaned {
		t.Error("the applier's cleanup was not called after the run")
	}
	if got := strings.Join(order, ","); got != "end" {
		t.Errorf("order = %s, want end (and no price notice)", got)
	}
	i, j := strings.Index(stdout, "END-MARKER"), strings.Index(stdout, "\nwt: claude · profile/cloud · ")
	if i < 0 || j < 0 || i > j {
		t.Errorf("stdout = %q, want the session ended before a summary line naming the profile", stdout)
	}
	if strings.Contains(stdout, "survey ·") {
		t.Errorf("stdout = %q, want no survey after a profile session", stdout)
	}
}

// TestRunSlotAgentCmdReturnsARunError verifies an agent that could not be
// run at all comes back as an error after the post-exit flow has run, so the
// client session registered for the launch is ended, not left to its TTL.
func TestRunSlotAgentCmdReturnsARunError(t *testing.T) {
	slotLaunchEnv(t)
	ended := false
	row := slotRow(slotFixtureCheck(0, "cloud", "opus", slotCloud()))

	var err error
	captureFile(t, &os.Stdout, func() {
		err = runSlotAgentCmd(exec.Command(filepath.Join(t.TempDir(), "no-such-agent")), "claude", row, func() error { return nil }, runTail{End: func() { ended = true }})
	})
	if err == nil {
		t.Fatal("err = nil, want the run error")
	}
	if !ended {
		t.Error("the client session was not ended after a failed run")
	}
}

// TestLaunchSlotProfileRecordsTheProfile verifies the bookkeeping of a profile
// launch: one usage event under the profile's id, last-picked.state naming
// the profile, rotation.state byte-identical. The profile then shows its
// launches in the picker and in `wt stats`, and the rotation of single-model
// launches is not disturbed.
func TestLaunchSlotProfileRecordsTheProfile(t *testing.T) {
	r, _ := slotRig(t, map[string]string{"opus": "openrouter/a", "sonnet": "openrouter/a", "haiku": "openrouter/b"})
	stateDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "agent-wt")
	rotationState := filepath.Join(stateDir, "rotation.state")
	if err := os.WriteFile(rotationState, []byte("openrouter/b\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var err error
	captureFile(t, &os.Stdout, func() {
		err = launchFilteredRemote(t.Context(), r.a, "claude", t.TempDir(), false, "", "", "profile/mix", true, nil, "", false, nil)
	})
	if err != nil {
		t.Fatalf("launch error = %v", err)
	}

	counts := usage.NewStoreAt(stateDir).CountsForAgent("claude", []string{"profile/mix", "openrouter/a"})
	if counts["profile/mix"].ThirtyDay != 1 || counts["openrouter/a"].ThirtyDay != 0 {
		t.Errorf("usage = %+v, want one launch under profile/mix and none under a slot model", counts)
	}
	if data, _ := os.ReadFile(filepath.Join(stateDir, "last-picked.state")); string(data) != "profile/mix\n" {
		t.Errorf("last-picked.state = %q, want %q", data, "profile/mix\n")
	}
	if data, _ := os.ReadFile(rotationState); string(data) != "openrouter/b\n" {
		t.Errorf("rotation.state = %q, want it byte-identical (%q)", data, "openrouter/b\n")
	}
}

// TestSlotProfileLaunchSkipsAutomaticProfiles verifies a profile launch never
// resolves, prompts for or applies an automatic profile, even when
// profiles.toml has an entry that matches one of the slot models. The two
// kinds of profile do not stack: an automatic env or settings file applied on
// top could redirect a slot.
func TestSlotProfileLaunchSkipsAutomaticProfiles(t *testing.T) {
	r, built := slotRig(t, map[string]string{"sonnet": "openrouter/a"})
	store := profiles.Store{Enabled: true, Profiles: []profiles.Profile{{
		Agent: "claude", Match: "provider", Provider: "openrouter", Env: map[string]string{"WT_TEST_AUTO_PROFILE": "1"},
	}}}
	loads, prompts := 0, 0
	oldLoad, oldConfirm := loadProfileStore, confirmProfile
	loadProfileStore = func() (profiles.Store, error) { loads++; return store, nil }
	confirmProfile = func(profiles.ResolvedProfile) (bool, error) { prompts++; return true, nil }
	t.Cleanup(func() { loadProfileStore, confirmProfile = oldLoad, oldConfirm })

	// The already-loaded store is handed in too, as runLaunchPath hands it to
	// every launch: the profile path must ignore it.
	pp := &precomputedProfiles{store: store}
	var err error
	captureFile(t, &os.Stdout, func() {
		err = launchFilteredRemote(t.Context(), r.a, "claude", t.TempDir(), false, "", "", "profile/mix", true, nil, "", false, pp)
	})
	if err != nil {
		t.Fatalf("launch error = %v", err)
	}
	if loads != 0 || prompts != 0 {
		t.Errorf("profiles.toml loaded %d times and prompted %d times, want 0 and 0", loads, prompts)
	}
	if len(*built) != 1 {
		t.Fatalf("built %d commands, want 1", len(*built))
	}
	if slices.Contains((*built)[0].Env, "WT_TEST_AUTO_PROFILE=1") {
		t.Errorf("cmd.Env = %v: the automatic profile was applied to a slot-profile launch", (*built)[0].Env)
	}
}

// TestSlotLaunchWritesClaim checks a slot-profile launch claims the worktree
// it runs in, as every other launch does: the claim names the worktree's top
// level in git's text, its branch and the agent, live exactly while the agent
// runs. Why: the slot flow is a launch path like any other (Task 405), and a
// session it starts must be visible to the "in use by another session"
// warning.
func TestSlotLaunchWritesClaim(t *testing.T) {
	r, _ := slotRig(t, map[string]string{"sonnet": "openrouter/a"})
	root := sharedRootRepo(t)
	claimCopy := filepath.Join(t.TempDir(), "claim")
	old := buildSlotGatewayCmd
	buildSlotGatewayCmd = func(_ profiles.SlotLaunch, wt string, _ bool, _ agents.Gateway, _ []string) (*exec.Cmd, error) {
		cmd := exec.Command("/bin/sh", "-c",
			"d=$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null) && cp \"$d/wt-sessions/"+strconv.Itoa(os.Getpid())+"\" '"+claimCopy+"' 2>/dev/null\nexit 0")
		cmd.Dir = wt
		return cmd, nil
	}
	t.Cleanup(func() { buildSlotGatewayCmd = old })

	var err error
	captureFile(t, &os.Stdout, func() {
		err = launchFilteredRemote(t.Context(), r.a, "claude", root, false, "", "", "profile/mix", true, nil, "", false, nil)
	})
	if err != nil {
		t.Fatalf("launch error = %v", err)
	}
	claim := readClaimFields(t, claimCopy)
	if claim == nil {
		t.Fatal("no claim was written for the slot-profile launch")
	}
	if got := claim["path"]; got != root {
		t.Errorf("claim path = %q; want the launched worktree %q", got, root)
	}
	if got := claim["branch"]; got != "main" {
		t.Errorf("claim branch = %q; want %q", got, "main")
	}
	if got := claim["agent"]; got != "claude" {
		t.Errorf("claim agent = %q; want %q", got, "claude")
	}
	if fileExists(ownClaim(root)) {
		t.Error("the claim outlived the agent")
	}
}

// launchCobraCmd is a command with the two flags runLaunchPath reads.
func launchCobraCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().StringP("model", "M", "", "")
	cmd.Flags().Bool("yolo", false, "")
	return cmd
}

// TestSlotWarnings verifies the warning lines of a launch: the load error;
// one line per profile that has no usable name or agent, naming its position
// in the file; and one line per modelOverrides target of a
// passing profile that is not one of its slot models (#412), the launch-time
// channel that warning reaches both launch paths through. A profile that
// merely fails validation gets no line here: it has a blocked row that
// carries its reason.
func TestSlotWarnings(t *testing.T) {
	usable := slotFixtureCheck(0, "cloud", "opus", slotCloud())
	blocked := slotFixtureCheck(1, "broken", "opus", slotCloud())
	blocked.Err = errors.New("default is missing")
	unusable := profiles.SlotCheck{Index: 2, Profile: profiles.SlotProfile{Agent: "claude"}, Err: errors.New("name is missing")}
	overriding := slotFixtureCheck(3, "overriding", "opus", slotCloud())
	overriding.Profile.ConfigContent = map[string]any{"modelOverrides": map[string]any{
		"claude-opus-4-1": "openrouter/a", "claude-sonnet-4-6": "openrouter/z"}}
	routedOnly := slotFixtureCheck(4, "routed-only", "opus", slotCloud())
	routedOnly.Profile.ConfigContent = map[string]any{"modelOverrides": map[string]any{"claude-opus-4-1": "openrouter/b"}}
	blockedOverriding := overriding
	blockedOverriding.Err = errors.New("default is missing")
	cases := []struct {
		name string
		a    *app
		want []string
	}{
		{"nothing wrong", &app{slotChecks: []profiles.SlotCheck{usable, blocked}}, nil},
		{"a modelOverrides target that is not a slot model, once",
			&app{slotChecks: []profiles.SlotCheck{usable, overriding, routedOnly}},
			[]string{`wt: profile/overriding (claude): config_content.modelOverrides["claude-sonnet-4-6"] targets "openrouter/z", which is not one of the profile's slot models, so wt does not make sure it is routed`}},
		{"a blocked profile's modelOverrides says nothing more",
			&app{slotChecks: []profiles.SlotCheck{blockedOverriding}}, nil},
		{"no file", &app{}, nil},
		{"the file does not parse",
			&app{slotLoadErr: errors.New("parse slot-profiles.toml: toml: line 2: unexpected EOF")},
			[]string{"wt: parse slot-profiles.toml: toml: line 2: unexpected EOF — slot profiles are unavailable"}},
		{"a profile with no usable name",
			&app{slotChecks: []profiles.SlotCheck{usable, unusable}},
			[]string{"wt: slot-profiles.toml[2]: name is missing — skipped (see 'wt profile list')"}},
		{"a file whose only content is under an unknown key",
			&app{slotUnknown: []string{"profiles"}},
			[]string{`wt: slot-profiles.toml: unknown key "profiles" — ignored`}},
		{"unknown keys beside a usable profile, one line each",
			&app{slotChecks: []profiles.SlotCheck{usable}, slotUnknown: []string{"top", "profile.envs"}},
			[]string{
				`wt: slot-profiles.toml: unknown key "top" — ignored`,
				`wt: slot-profiles.toml: unknown key "profile.envs" — ignored`,
			}},
		{"an unusable profile alone",
			&app{slotChecks: []profiles.SlotCheck{unusable}},
			[]string{"wt: slot-profiles.toml[2]: name is missing — skipped (see 'wt profile list')"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := slotWarningsRemote(tc.a); !slices.Equal(got, tc.want) {
				t.Errorf("slotWarningsRemote() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRunLaunchPathWarnsAboutSlotProfilesOncePerRun verifies who sees the
// warnings: a launch with no agent chosen yet, or with an agent that has
// slots, prints each line once; a shell launch prints none, since a slot
// profile could never apply to it.
func TestRunLaunchPathWarnsAboutSlotProfilesOncePerRun(t *testing.T) {
	cases := []struct {
		agent string
		want  int
	}{{"claude", 1}, {"", 1}, {"shell", 0}}
	for _, tc := range cases {
		t.Run("agent="+tc.agent, func(t *testing.T) {
			stubStdinTTY(t, false)
			oldLaunch := launchCommand
			launchCommand = func(string, string, *config.Config, bool, bool, []string, *precomputedProfiles) error {
				return nil
			}
			t.Cleanup(func() { launchCommand = oldLaunch })
			// No model server: a model-driven agent's launch is refused,
			// and no agent's is the picker's terminal refusal; the
			// warnings are printed before either.
			a := &app{slotLoadErr: errors.New("parse slot-profiles.toml: toml: line 2: unexpected EOF")}

			stderr := captureFile(t, &os.Stderr, func() {
				_ = runLaunchPath(launchCobraCmd(), a, tc.agent, "", "", "", nil, ".", "")
			})

			if got := strings.Count(stderr, "slot profiles are unavailable"); got != tc.want {
				t.Errorf("warning printed %d times, want %d; stderr = %q", got, tc.want, stderr)
			}
		})
	}
}

// TestRunLaunchPathRepeatsSlotWarningsAfterTheTUI verifies the warnings on
// the TUI path: they are printed before the TUI opens, where the alternate
// screen then covers them, and the same lines are printed again once the TUI
// has returned and the terminal is the user's again. Without the second
// print a user with a typo in slot-profiles.toml sees a picker with no
// profile rows and nothing on screen that says why. Both ways into the TUI
// are covered: the worktree picker (no launch path) and the model picker.
func TestRunLaunchPathRepeatsSlotWarningsAfterTheTUI(t *testing.T) {
	const warning = "wt: parse slot-profiles.toml: toml: line 2: unexpected EOF — slot profiles are unavailable\n" +
		"wt: slot-profiles.toml: unknown key \"top\" — ignored\n"
	cases := []struct{ name, launchPath string }{
		{"the worktree picker", ""},
		{"the model picker", "."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubStdinTTY(t, true)
			oldTUI, oldBuild := tuiRun, buildGatewayCmd
			buildGatewayCmd = func(string, config.Model, string, bool, agents.Gateway, []config.Model, []string) (*exec.Cmd, agents.LaunchInfo, error) {
				t.Error("the launch went past the picker")
				return nil, agents.LaunchInfo{}, errors.New("no launch")
			}
			tuiRun = func(bool, bool, string, string, string, string, string, []string, themes.Theme, string, tui.ModelClient, *config.Config, tui.ProfileApplier, tui.SlotProfiles, tui.RemoveFuncsBuilder) error {
				fmt.Fprintln(os.Stderr, "<the TUI ran>")
				return errors.New("the TUI's own error")
			}
			t.Cleanup(func() { tuiRun, buildGatewayCmd = oldTUI, oldBuild })
			// Two launchable models on the model server: with no -M the
			// model picker decides.
			f := newFakeModelServer(t, wireRow("openrouter/a", "launch", 1), wireRow("openrouter/b", "launch", 2))
			a := pointAtFake(t, f, "")
			a.slotUnknown = []string{"top"}
			a.slotLoadErr = errors.New("parse slot-profiles.toml: toml: line 2: unexpected EOF")

			var err error
			stderr := captureFile(t, &os.Stderr, func() {
				err = runLaunchPath(launchCobraCmd(), a, "claude", "", "", "", nil, tc.launchPath, "")
			})

			if want := warning + "<the TUI ran>\n" + warning; stderr != want {
				t.Errorf("stderr =\n%s\nwant\n%s", stderr, want)
			}
			if err == nil || err.Error() != "the TUI's own error" {
				t.Errorf("err = %v, want the TUI's own error passed through", err)
			}
		})
	}
}

// TestBrokenSlotFileLeavesModelLaunchesAlone is Review Focus 1. With a TOML
// typo in slot-profiles.toml, or one profile with a malformed name, a pinned
// single-model launch on the model server behaves exactly as before (it runs
// and records the rotation), wt prints exactly one warning line for the
// problem, and `-M profile/<name>` (no usable slot profile by that name, so
// a model id the server does not know) exits 1 naming the pin. A
// hand-written file must never be able to break the launches that do not
// use it.
func TestBrokenSlotFileLeavesModelLaunchesAlone(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		pin     string
		warning string
	}{
		{"a TOML typo", "[[profile]]\nname = \"mixed\n", "profile/mixed", "slot profiles are unavailable"},
		{"a malformed name",
			"[[profile]]\nname = \"Bad_Name\"\nagent = \"claude\"\ndefault = \"opus\"\n\n[profile.slots]\nopus = \"openrouter/a\"\n",
			"profile/Bad_Name", "skipped (see 'wt profile list')"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeSlotProfilesTOML(t, tc.body)
			stateDir := filepath.Dir(path)
			worktree := t.TempDir()
			fakeAgentOnPath(t, "claude")
			// The slot fixture's models on a model server config.toml
			// names, in the same config home as the slot file.
			nameServerInConfigHome(t, slotsServer(t))

			// The app as wt builds it: newApp reads the slot file from the
			// test's config home.
			a, aerr := newApp()
			if aerr != nil {
				t.Fatalf("newApp() error = %v: a broken slot file must not stop wt", aerr)
			}

			rotationState := func() string {
				data, _ := os.ReadFile(filepath.Join(stateDir, "rotation.state"))
				return strings.TrimSpace(string(data))
			}

			// A pinned single-model launch: it runs, and the warning is
			// printed once.
			var err error
			var stderr string
			captureFile(t, &os.Stdout, func() {
				stderr = captureFile(t, &os.Stderr, func() {
					err = runLaunchPath(launchCobraCmd(), a, "claude", "openrouter/a", "", "", nil, worktree, "")
				})
			})
			if err != nil {
				t.Fatalf("pinned launch error = %v", err)
			}
			if got := rotationState(); got != "openrouter/a" {
				t.Errorf("rotation.state = %q after the pinned launch, want openrouter/a", got)
			}
			if got := strings.Count(stderr, tc.warning); got != 1 {
				t.Errorf("warning printed %d times, want exactly once; stderr = %q", got, stderr)
			}
			slotLines := 0
			for _, line := range strings.Split(stderr, "\n") {
				if strings.Contains(line, "slot") {
					slotLines++
				}
			}
			if slotLines != 1 {
				t.Errorf("%d stderr lines mention slot profiles, want one; stderr = %q", slotLines, stderr)
			}

			// The profile itself cannot be pinned: it is not a slot
			// profile, so the pin goes to the model server, which has no
			// such model.
			captureFile(t, &os.Stderr, func() {
				err = runLaunchPath(launchCobraCmd(), a, "claude", tc.pin, "", "", nil, worktree, "")
			})
			if err == nil || exitcode.Of(err) != 1 || !strings.Contains(err.Error(), tc.pin) {
				t.Errorf("profile pin error = %v, want a refusal naming %s, exit 1", err, tc.pin)
			}
		})
	}
}

// TestRunLaunchPathLaunchesAPinnedSlotProfile verifies the whole non-TUI path
// from runLaunchPath: `-A claude -M profile/<name>` with a worktree reaches
// the profile launch and runs the agent, and a pin of a blocked profile
// returns its reason, which main turns into a non-zero exit.
func TestRunLaunchPathLaunchesAPinnedSlotProfile(t *testing.T) {
	r := newRemoteRig(t, slotWireRows()...)
	worktree := t.TempDir()
	sentinel := filepath.Join(t.TempDir(), "agent-ran")
	t.Setenv("WT_FAKE_MARK", sentinel) // the rig's fake claude touches it
	a := r.a
	a.slotChecks = []profiles.SlotCheck{
		slotFixtureCheck(0, "cloud", "opus", slotCloud()),
		slotFixtureCheck(1, "broken", "opus", map[string]string{"opus": "openrouter/gone"}),
	}

	var err error
	captureFile(t, &os.Stdout, func() {
		err = runLaunchPath(launchCobraCmd(), a, "claude", "profile/cloud", "", "", nil, worktree, "")
	})
	if err != nil {
		t.Fatalf("runLaunchPath() error = %v", err)
	}
	if _, statErr := os.Stat(sentinel); statErr != nil {
		t.Error("the agent command did not run for a pinned launch profile")
	}

	err = runLaunchPath(launchCobraCmd(), a, "claude", "profile/broken", "", "", nil, worktree, "")
	if err == nil || !strings.HasPrefix(err.Error(), "slot opus: openrouter/gone is not in the model server's catalog") {
		t.Errorf("blocked pin error = %v, want the row's reason", err)
	}
}

// TestRunLaunchPathRefusesANonStringOverrideTarget follows a claude profile
// whose config_content.modelOverrides has a number target, read from the file
// by the real drivers, to a `-A claude -M profile/<name>` launch (#418): the
// pin returns the block reason, the agent never runs, and no route is written
// and no command built, so the target never reaches the --settings payload.
// Before, the target only warned and the session started with
// {"modelOverrides":{"claude-sonnet-4-6":5}}, which Claude Code cannot use.
func TestRunLaunchPathRefusesANonStringOverrideTarget(t *testing.T) {
	r := newRemoteRig(t, slotWireRows()...)
	worktree := t.TempDir()
	sentinel := filepath.Join(t.TempDir(), "agent-ran")
	t.Setenv("WT_FAKE_MARK", sentinel) // the rig's fake claude touches it
	store, err := profiles.ParseSlots([]byte("[[profile]]\nname = \"numbered\"\nagent = \"claude\"\ndefault = \"opus\"\n" +
		"[profile.slots]\nopus = \"openrouter/a\"\n[profile.config_content.modelOverrides]\nclaude-sonnet-4-6 = 5\n"))
	if err != nil {
		t.Fatal(err)
	}
	a := r.a
	a.slotChecks = profiles.ValidateSlots(store, agents.SlotDrivers())

	err = runLaunchPath(launchCobraCmd(), a, "claude", "profile/numbered", "", "", nil, worktree, "")
	const want = `config_content.modelOverrides["claude-sonnet-4-6"] must be a model id string`
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("runLaunchPath() error = %v, want %q", err, want)
	}
	if _, statErr := os.Stat(sentinel); statErr == nil {
		t.Error("the agent ran for a profile with a non-string modelOverrides target")
	}
	if n := r.f.count("POST /control/v1/start") + r.f.count("POST /control/v1/sessions"); n != 0 {
		t.Errorf("the refused launch asked the model server to start or register: %v", r.f.reqs())
	}
}
