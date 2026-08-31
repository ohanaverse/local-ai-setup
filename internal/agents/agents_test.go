package agents

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/session"
)

func nativeModel(agent string) config.Model {
	return config.Model{ID: agent + "/native", ProviderID: agent, ModelName: "native", Location: config.LocationCloud, Native: true}
}

// namedNativeModel returns a non-sentinel native-provider model (e.g.
// "claude/opus"): same provider key as the sentinel but with a real
// model name that should be passed via --model.
func namedNativeModel(provider, name string) config.Model {
	return config.Model{ID: provider + "/" + name, ProviderID: provider, ModelName: name, Location: config.LocationCloud, Native: true}
}

func cloudModel(id string) config.Model {
	return config.Model{ID: id, ModelName: id, Location: config.LocationCloud}
}

func localModel(id string) config.Model {
	return config.Model{ID: id, ModelName: id, Location: config.LocationLocal}
}

func directGateway() Gateway { return Gateway{Mode: "direct"} }

// ollamaCloudModel mirrors cloudModel but with a real ProviderID, so the
// provider-keyed driver dispatch reaches the ollama branch (cloudModel
// leaves ProviderID empty, which silently routes through the native
// branch and breaks the "route through ollama" tests).
func ollamaCloudModel(name string) config.Model {
	return config.Model{ID: "ollama/" + name, ProviderID: "ollama", ModelName: name, Location: config.LocationCloud}
}

// Names must return every agent registered in the package. A mismatch means
// the registry is incomplete and the CLI will be unable to list or launch
// one or more agents.
func TestNames(t *testing.T) {
	names := Names()
	want := map[string]bool{"claude": true, "codex": true, "copilot": true, "opencode": true, "pi": true, "agy": true, "shell": true}
	if len(names) != len(want) {
		t.Fatalf("Names() = %v, want %d agents", names, len(want))
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected agent %q", n)
		}
	}
}

// ByName must return nil for an unknown agent name so callers (the CLI, the
// TUI) can distinguish between supported and unsupported agents rather than
// panicking on a nil dereference.
func TestByNameUnknown(t *testing.T) {
	if d := ByName("nope"); d != nil {
		t.Fatalf("ByName(nope) = %v, want nil", d)
	}
}

// Gateway.BaseURL must strip any trailing slash from the configured URL
// before drivers append their protocol-specific /v1 or /v1/ suffix.
// Without this, a user-configured URL like "http://localhost:4000/" would
// produce double slashes (e.g. "http://localhost:4000//v1") and the agent
// would fail to connect. TrimRight (not TrimSuffix) so a double-slash typo
// is normalized too.
func TestGatewayBaseURLTrim(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"http://localhost:4000", "http://localhost:4000"},
		{"http://localhost:4000/", "http://localhost:4000"},
		{"http://localhost:4000//", "http://localhost:4000"},
		{"", ""},
	}
	for _, c := range cases {
		got := Gateway{URL: c.url}.BaseURL()
		if got != c.want {
			t.Errorf("BaseURL(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

// The Claude driver handles three cases: native sentinel (no args/env,
// clears the inherited gateway vars), native named (passes --model,
// clears the inherited gateway vars), and ollama cloud (sets the
// gateway env var and passes --model). Getting any of these wrong means
// the launched claude process receives the wrong CLI flags or routes
// through the wrong provider.
func TestClaude(t *testing.T) {
	d := ByName("claude")
	if d == nil {
		t.Fatal("claude driver not registered")
	}

	// Native sentinel — no args/env, but clears the inherited ANTHROPIC_*
	// gateway vars so the native subscription is used instead of routing
	// to ollama.
	lc := d.Build(nativeModel("claude"), false, directGateway())
	if lc.Bin != "claude" || len(lc.Args) != 0 || len(lc.Env) != 0 {
		t.Errorf("native build = %+v, want bare claude", lc)
	}
	for _, k := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"} {
		if !slices.Contains(lc.ClearEnv, k) {
			t.Errorf("native ClearEnv = %v, want it to include %q", lc.ClearEnv, k)
		}
	}

	// Ollama cloud — env gateway + --model.
	lc = d.Build(ollamaCloudModel("deepseek-v4-pro:cloud"), false, directGateway())
	if len(lc.Args) != 2 || lc.Args[0] != "--model" || lc.Args[1] != "deepseek-v4-pro:cloud" {
		t.Errorf("cloud args = %v, want [--model deepseek-v4-pro:cloud]", lc.Args)
	}
	if !hasEnv(lc.Env, "ANTHROPIC_BASE_URL=http://localhost:11434") {
		t.Errorf("cloud env missing gateway: %v", lc.Env)
	}

	// Yolo flag prepended.
	lc = d.Build(ollamaCloudModel("x"), true, directGateway())
	if len(lc.Args) < 1 || lc.Args[0] != "--dangerously-skip-permissions" {
		t.Errorf("yolo args = %v, want leading --dangerously-skip-permissions", lc.Args)
	}
}

// codex ollama-routed launches must declare the Ollama model provider via
// inline -c overrides (see codex.go's ollamaProvider block). This helper
// builds the exact expected args for a given bare model name so the
// assertions stay in lockstep with the driver without hardcoding the provider
// name/URL three times. The provider name is "agent-wt" (namespaced so a
// user's own [model_providers.ollama-launch] can't leak into our set) and the
// base_url uses the OpenAI-compatible /v1/ path.
func codexProviderArgs(name string) []string {
	return []string{
		"-c", "model_provider=agent-wt",
		"-c", `model_providers.agent-wt.name="Ollama"`,
		"-c", "model_providers.agent-wt.base_url=\"" + codexDriver{}.OllamaURL() + "\"",
		"-c", `model_providers.agent-wt.wire_api="responses"`,
		"--model", name,
	}
}

// Codex is a cloud-only agent. Native models pass nothing; ollama-routed
// models declare the Ollama provider via inline -c overrides and then pass
// --model with the bare name. Without the provider block codex defaults to
// the "openai" provider and prompts to sign in. Verify both paths and that no
// env vars are set, and that yolo prepends the approval-skip flag before the
// provider block.
func TestCodex(t *testing.T) {
	d := ByName("codex")
	if d == nil {
		t.Fatal("codex driver not registered")
	}
	lc := d.Build(cloudModel("deepseek-v4-pro:cloud"), false, directGateway())
	if !slices.Equal(lc.Args, codexProviderArgs("deepseek-v4-pro:cloud")) {
		t.Errorf("args = %v, want %v", lc.Args, codexProviderArgs("deepseek-v4-pro:cloud"))
	}
	if len(lc.Env) != 0 {
		t.Errorf("env = %v, want none", lc.Env)
	}
	if d.Build(nativeModel("codex"), false, directGateway()).Args != nil {
		t.Errorf("native build should have no args")
	}
	// yolo prepends the approval-skip flag ahead of the provider block.
	yl := d.Build(cloudModel("deepseek-v4-pro:cloud"), true, directGateway())
	if len(yl.Args) < 1 || yl.Args[0] != "--dangerously-bypass-approvals-and-sandbox" {
		t.Errorf("yolo args = %v, want first flag to be the approval-skip", yl.Args)
	}
}

// Copilot never receives --model on the CLI. Instead it gets the COPILOT_*
// env vars that tell Copilot CLI which provider and model to use. For
// Ollama-routed models the base URL must include the `/v1` path and the
// wire API must be `responses`, matching `ollama launch copilot`.
// Native models must produce no env vars at all.
func TestCopilot(t *testing.T) {
	d := ByName("copilot")
	if d == nil {
		t.Fatal("copilot driver not registered")
	}
	lc := d.Build(ollamaCloudModel("deepseek-v4-pro:cloud"), false, directGateway())
	if len(lc.Args) != 0 {
		t.Errorf("copilot should not pass --model, got args %v", lc.Args)
	}
	if !hasEnv(lc.Env, "COPILOT_MODEL=deepseek-v4-pro:cloud") {
		t.Errorf("env missing COPILOT_MODEL: %v", lc.Env)
	}
	if !hasEnv(lc.Env, "COPILOT_PROVIDER_BASE_URL=http://localhost:11434/v1") {
		t.Errorf("env missing base url: %v", lc.Env)
	}
	if !hasEnv(lc.Env, "COPILOT_PROVIDER_WIRE_API=responses") {
		t.Errorf("env missing wire api: %v", lc.Env)
	}
	if len(d.Build(nativeModel("copilot"), false, directGateway()).Env) != 0 {
		t.Errorf("native build should have no env")
	}
	// Native must clear the inherited COPILOT_* gateway vars so the native
	// subscription is used instead of routing to ollama.
	clear := d.Build(nativeModel("copilot"), false, directGateway()).ClearEnv
	for _, k := range []string{"COPILOT_PROVIDER_BASE_URL", "COPILOT_PROVIDER_API_KEY", "COPILOT_PROVIDER_WIRE_API", "COPILOT_MODEL"} {
		if !slices.Contains(clear, k) {
			t.Errorf("native ClearEnv = %v, want it to include %q", clear, k)
		}
	}
}

// OpenCode receives its model configuration via a single
// OPENCODE_CONFIG_CONTENT env var containing inline JSON. After the
// native-provider alignment, opencode is ollama-only — there is no native
// branch to test.
func TestOpenCode(t *testing.T) {
	d := ByName("opencode")
	if d == nil {
		t.Fatal("opencode driver not registered")
	}
	lc := d.Build(ollamaCloudModel("deepseek-v4-pro:cloud"), false, directGateway())
	if len(lc.Args) != 0 {
		t.Errorf("opencode should not pass --model, got args %v", lc.Args)
	}
	if len(lc.Env) != 1 || !strings.Contains(lc.Env[0], "OPENCODE_CONFIG_CONTENT=") {
		t.Fatalf("env = %v, want OPENCODE_CONFIG_CONTENT", lc.Env)
	}
	if !strings.Contains(lc.Env[0], `"model":"ollama/deepseek-v4-pro:cloud"`) {
		t.Errorf("config missing model: %s", lc.Env[0])
	}
}

// writePiModels points $HOME at a temp dir and writes a models.json there, so
// piDriver.Build (which resolves the real path via os.UserHomeDir) reads the
// test fixture instead of the user's real catalog.
func writePiModels(t *testing.T, content string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".pi", "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Pi has no yolo flag (the pi CLI doesn't support skipping permissions).
func TestPiYoloFlag(t *testing.T) {
	d := ByName("pi")
	if d == nil {
		t.Fatal("pi driver not registered")
	}
	if d.YoloFlag() != "" {
		t.Errorf("pi yolo flag = %q, want empty", d.YoloFlag())
	}
}

// Pi passes --model <ModelName> only when the model is present in models.json
// and marked _launch: true. Passing --model for an unverified model would
// launch a model pi doesn't intend to use.
func TestPiBuildVerified(t *testing.T) {
	writePiModels(t, `{"providers":{"ollama":{"models":[{"_launch":true,"id":"deepseek-v4-pro:cloud"}]}}}`)
	d := ByName("pi")
	lc := d.Build(config.Model{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud"}, false, directGateway())
	if len(lc.Args) != 2 || lc.Args[0] != "--model" || lc.Args[1] != "deepseek-v4-pro:cloud" {
		t.Errorf("args = %v, want [--model deepseek-v4-pro:cloud]", lc.Args)
	}
	if lc.Warn != "" {
		t.Errorf("warn = %q, want empty for a verified model", lc.Warn)
	}
}

// Pi falls back to its default model (no --model) and sets a warning when the
// model is present but not marked _launch: true.
func TestPiBuildNotVerified(t *testing.T) {
	writePiModels(t, `{"providers":{"ollama":{"models":[{"_launch":false,"id":"deepseek-v4-pro:cloud"}]}}}`)
	d := ByName("pi")
	lc := d.Build(config.Model{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud"}, false, directGateway())
	if len(lc.Args) != 0 {
		t.Errorf("args = %v, want none (fallback to default)", lc.Args)
	}
	if lc.Warn == "" {
		t.Error("warn should be set when falling back")
	}
}

// Pi falls back to its default model when models.json is missing entirely.
func TestPiBuildMissingFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no models.json created
	d := ByName("pi")
	lc := d.Build(config.Model{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud"}, false, directGateway())
	if len(lc.Args) != 0 {
		t.Errorf("args = %v, want none (fallback to default)", lc.Args)
	}
	if lc.Warn == "" {
		t.Error("warn should be set when models.json is missing")
	}
}

// Pi native models produce no args and no warning.
func TestPiBuildNative(t *testing.T) {
	d := ByName("pi")
	lc := d.Build(nativeModel("pi"), false, directGateway())
	if len(lc.Args) != 0 || lc.Warn != "" {
		t.Errorf("native build = %+v, want bare pi", lc)
	}
}

// Agy is model-agnostic — the model is chosen inside its TUI. The driver must
// ignore the model entirely and only handle the yolo flag. A broken driver
// could accidentally pass --model to agy, which would crash or misbehave.
func TestAgy(t *testing.T) {
	d := ByName("agy")
	if d == nil {
		t.Fatal("agy driver not registered")
	}
	// Model is ignored entirely.
	lc := d.Build(cloudModel("anything"), false, directGateway())
	if len(lc.Args) != 0 || len(lc.Env) != 0 {
		t.Errorf("agy build = %+v, want bare agy", lc)
	}
	lc = d.Build(cloudModel("anything"), true, directGateway())
	if len(lc.Args) != 1 || lc.Args[0] != "--dangerously-skip-permissions" {
		t.Errorf("agy yolo args = %v, want [--dangerously-skip-permissions]", lc.Args)
	}
}

// Installed must return true for binaries on PATH (e.g. git) and false for
// ones that don't exist. This is the gate check the TUI uses to grey out
// unavailable agents.
func TestInstalled(t *testing.T) {
	if !Installed("git") {
		t.Error("expected git to be on PATH")
	}
	if Installed("not-a-real-binary-ever-12345") {
		t.Error("expected unknown binary to not be installed")
	}
}

// Command builds an exec.Cmd with the correct binary, args, working
// directory, and merged environment. This is the final step before the
// tool replaces itself with the agent process.
func TestCommand(t *testing.T) {
	d := ByName("pi")
	m := cloudModel("test-model")
	workdir := "/tmp"

	cmd, err := Command(d, m, false, directGateway(), workdir)
	if err != nil {
		// pi may not be installed; that's fine, just verify the error is clear.
		if !strings.Contains(err.Error(), "not installed") {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	if cmd.Dir != workdir {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, workdir)
	}
	// cmd.Env should start with os.Environ() (inherited env).
	if len(cmd.Env) == 0 {
		t.Error("cmd.Env should include inherited environment")
	}
}

// Command must strip LaunchCmd.ClearEnv names from the inherited environment
// before launching. This is how native launches avoid inheriting the ollama
// gateway vars (ANTHROPIC_BASE_URL etc.) from a parent shell that has them
// exported — without it, a native claude launch would silently route to the
// gateway instead of the native subscription.
func TestCommandClearsInheritedEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "http://localhost:11434")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "ollama")

	cmd, err := Command(clearEnvDriver{}, config.Model{}, false, directGateway(), "/tmp")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if hasEnvKey(cmd.Env, "ANTHROPIC_BASE_URL") {
		t.Errorf("cmd.Env still contains ANTHROPIC_BASE_URL: %v", cmd.Env)
	}
	if hasEnvKey(cmd.Env, "ANTHROPIC_AUTH_TOKEN") {
		t.Errorf("cmd.Env still contains ANTHROPIC_AUTH_TOKEN: %v", cmd.Env)
	}
}

// BuildLaunchCmd must append extraArgs to cmd.Args for regular agents (those
// that do not implement ArgSetter). Failing to append means passthrough args
// like `--verbose` after `--` are silently dropped and never reach the agent.
func TestBuildLaunchCmdAppendsExtraArgs(t *testing.T) {
	// Use a test-only driver backed by bash (always on PATH) that does not
	// implement ArgSetter, exercising the regular-agent append path.
	register("_test_regular", func() Driver { return regularTestDriver{} })
	t.Cleanup(func() { delete(registry, "_test_regular") })

	cmd, err := BuildLaunchCmd("_test_regular", config.Model{}, "/tmp", false, nil, nil, []string{"--foo", "--bar"})
	if err != nil {
		t.Fatalf("BuildLaunchCmd: %v", err)
	}
	args := cmd.Args
	if len(args) < 2 {
		t.Fatalf("cmd.Args = %v, want at least [--foo --bar] appended", args)
	}
	last2 := args[len(args)-2:]
	if last2[0] != "--foo" || last2[1] != "--bar" {
		t.Errorf("cmd.Args suffix = %v, want [--foo --bar]", last2)
	}
}

// BuildLaunchCmd must not double-append extraArgs for agents that implement
// ArgSetter (e.g. shell). The args must be passed to SetArgs only; if they
// were also appended to cmd.Args the shell would receive duplicate arguments,
// causing the command to malform or fail at runtime.
func TestBuildLaunchCmdShellNoDoubleAppend(t *testing.T) {
	cmd, err := BuildLaunchCmd("shell", config.Model{}, "/tmp", false, nil, nil, []string{"echo", "hello"})
	if err != nil {
		t.Fatalf("BuildLaunchCmd: %v", err)
	}
	// The shell driver execs the args directly as argv (no bash -c wrapping):
	// cmd.Args must be exactly [<echo path> hello] — no extra args appended.
	if len(cmd.Args) != 2 {
		t.Fatalf("cmd.Args = %v (len %d), want exactly 2 elements [<echo path> hello]", cmd.Args, len(cmd.Args))
	}
	if !strings.HasSuffix(cmd.Args[0], "echo") {
		t.Errorf("cmd.Args[0] = %q, want it to resolve to echo", cmd.Args[0])
	}
	if cmd.Args[1] != "hello" {
		t.Errorf("cmd.Args[1] = %q, want %q", cmd.Args[1], "hello")
	}
}

// regularTestDriver is a test-only Driver (no ArgSetter) that uses bash as its
// binary so tests always run without requiring a real agent to be installed.
type regularTestDriver struct{}

func (regularTestDriver) Build(_ config.Model, _ bool, _ Gateway) LaunchCmd {
	return LaunchCmd{Bin: "bash"}
}
func (regularTestDriver) YoloFlag() string { return "" }

func hasEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

// hasEnvKey reports whether env contains an entry for key (KEY=...).
func hasEnvKey(env []string, key string) bool {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

// clearEnvDriver is a test-only Driver that clears two env vars, used to
// verify Command strips them from the inherited environment.
type clearEnvDriver struct{}

func (clearEnvDriver) Build(_ config.Model, _ bool, _ Gateway) LaunchCmd {
	return LaunchCmd{Bin: "true", ClearEnv: []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN"}}
}
func (clearEnvDriver) YoloFlag() string { return "" }

// warnDriver is a test-only Driver whose Build returns a fixed warning, used
// to verify Command surfaces it on stderr.
type warnDriver struct{}

func (warnDriver) Build(m config.Model, yolo bool, gw Gateway) LaunchCmd {
	return LaunchCmd{Bin: "true", Warn: "test warning"}
}
func (warnDriver) YoloFlag() string { return "" }

// Command must print a non-empty LaunchCmd.Warn to stderr before returning the
// command. This is how the pi driver tells the user it fell back to pi's
// default model; if the warning is swallowed, the user silently gets a
// different model than they selected.
func TestCommandPrintsWarning(t *testing.T) {
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	_, cmdErr := Command(warnDriver{}, config.Model{}, false, directGateway(), "/tmp")
	w.Close()
	os.Stderr = old
	if cmdErr != nil {
		t.Fatalf("Command: %v", cmdErr)
	}
	out, _ := io.ReadAll(r)
	if !strings.Contains(string(out), "test warning") {
		t.Errorf("stderr = %q, want it to contain %q", string(out), "test warning")
	}
}

// ollamaPrefixedModel is the production-shaped config.Model: ID has the
// provider/model form (the registry key) while ModelName has the bare
// provider-side name. Existing tests use cloudModel(id) which collapses
// both fields to the same string, masking bugs where the wrong field is
// passed to the agent CLI. These regression tests use the real shape.
func ollamaPrefixedModel() config.Model {
	return config.Model{
		ID:         "ollama/deepseek-v4-pro:cloud",
		ModelName:  "deepseek-v4-pro:cloud",
		ProviderID: "ollama",
		Location:   config.LocationCloud,
	}
}

// Claude must pass --model with the bare ModelName, not the registry ID
// with the ollama/ prefix. With ANTHROPIC_BASE_URL pointing at the local
// Ollama gateway, the prefixed id reaches Ollama unchanged and Ollama
// reports "model not found" — the launch silently fails to use the model
// the user picked.
func TestClaudeOllamaPrefix(t *testing.T) {
	d := ByName("claude")
	if d == nil {
		t.Fatal("claude driver not registered")
	}
	lc := d.Build(ollamaPrefixedModel(), false, directGateway())
	if len(lc.Args) != 2 || lc.Args[0] != "--model" {
		t.Fatalf("args = %v, want [--model <name>]", lc.Args)
	}
	if lc.Args[1] != "deepseek-v4-pro:cloud" {
		t.Errorf("--model = %q, want %q (bare name, NOT the ollama/-prefixed id)", lc.Args[1], "deepseek-v4-pro:cloud")
	}
	for _, e := range lc.Env {
		if strings.HasPrefix(e, "ANTHROPIC_BASE_URL=") || strings.HasPrefix(e, "ANTHROPIC_AUTH_TOKEN=") {
			continue
		}
		if strings.Contains(e, "ollama/deepseek-v4-pro:cloud") {
			t.Errorf("env leaked the prefixed id: %s", e)
		}
	}
}

// Codex must pass --model with the bare ModelName. The ollama-launch
// profile (see docs/wt-agents/codex-wt.md) routes through Ollama; passing
// the prefixed id causes codex to ask Ollama for an unknown model.
// Codex must pass --model with the bare ModelName. The ollama provider block
// (see docs/wt-agents/codex-wt.md) routes through Ollama; passing the prefixed
// id as the --model value would make codex ask Ollama for a model it cannot
// resolve. With the provider block inserted, --model is the last arg pair.
func TestCodexOllamaPrefix(t *testing.T) {
	d := ByName("codex")
	if d == nil {
		t.Fatal("codex driver not registered")
	}
	lc := d.Build(ollamaPrefixedModel(), false, directGateway())
	if !slices.Equal(lc.Args, codexProviderArgs("deepseek-v4-pro:cloud")) {
		t.Fatalf("args = %v, want %v", lc.Args, codexProviderArgs("deepseek-v4-pro:cloud"))
	}
	// The --model value must be the bare provider-side name, not the registry
	// key.
	idx := slices.Index(lc.Args, "--model")
	if idx < 0 || idx+1 >= len(lc.Args) {
		t.Fatalf("--model missing or unterminated in args %v", lc.Args)
	}
	if got := lc.Args[idx+1]; got != "deepseek-v4-pro:cloud" {
		t.Errorf("--model value = %q, want %q (bare name)", got, "deepseek-v4-pro:cloud")
	}
	for _, a := range lc.Args {
		if strings.Contains(a, "ollama/") {
			t.Errorf("args leaked the prefixed id: %q", a)
		}
	}
}

// Copilot must set COPILOT_MODEL=<bare name>, not the prefixed id. The
// COPILOT_PROVIDER_* env vars route through Ollama; the model id reaches
// Ollama verbatim and must not carry the registry prefix.
func TestCopilotOllamaPrefix(t *testing.T) {
	d := ByName("copilot")
	if d == nil {
		t.Fatal("copilot driver not registered")
	}
	lc := d.Build(ollamaPrefixedModel(), false, directGateway())
	if !hasEnv(lc.Env, "COPILOT_MODEL=deepseek-v4-pro:cloud") {
		t.Errorf("COPILOT_MODEL missing or wrong; env = %v", lc.Env)
	}
	for _, e := range lc.Env {
		if strings.Contains(e, "COPILOT_MODEL=ollama/") {
			t.Errorf("COPILOT_MODEL leaked the prefixed id: %s", e)
		}
	}
}

// OpenCode is the one driver whose CLI uniquely requires the
// provider/model form (see docs/wt-agents/opencode-wt.md), so its inline
// JSON config deliberately constructs "ollama/" + m.ModelName — the bare
// provider-side name, NOT m.ID. The model field in OPENCODE_CONFIG_CONTENT
// must be "ollama/<ModelName>", never "ollama/ollama/<ModelName>". The
// double prefix is the trap to avoid: m.ID already carries the "ollama/"
// prefix (it is the registry key), so constructing "ollama/" + m.ID would
// yield "ollama/ollama/<ModelName>". A future refactor that switches this
// slot to m.ID would reintroduce the bug; this test locks in the correct
// m.ModelName shape.
//
// The same config also pins the baseURL to exactly what OllamaURL()
// returns — no extra /v1 suffix. Pre-refactor this code used
// config.OllamaBaseURL (no path) and the format string added /v1; when the
// refactor moved the /v1 into OllamaURL() itself, the format string was
// not updated, producing baseURL=http://localhost:11434/v1/v1 which the
// ollama gateway rejects. Parsing the JSON (not substring-matching) keeps
// both halves of the config honest.
func TestOpenCodeOllamaPrefix(t *testing.T) {
	d := ByName("opencode")
	if d == nil {
		t.Fatal("opencode driver not registered")
	}
	lc := d.Build(ollamaPrefixedModel(), false, directGateway())
	if len(lc.Env) != 1 || !strings.HasPrefix(lc.Env[0], "OPENCODE_CONFIG_CONTENT=") {
		t.Fatalf("env = %v, want single OPENCODE_CONFIG_CONTENT entry", lc.Env)
	}
	payload := strings.TrimPrefix(lc.Env[0], "OPENCODE_CONFIG_CONTENT=")
	var parsed struct {
		Model    string `json:"model"`
		Provider struct {
			Ollama struct {
				Options struct {
					BaseURL string `json:"baseURL"`
					APIKey  string `json:"apiKey"`
				} `json:"options"`
			} `json:"ollama"`
		} `json:"provider"`
	}
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		t.Fatalf("OPENCODE_CONFIG_CONTENT is not valid JSON: %v\npayload=%s", err, payload)
	}
	if got, want := parsed.Model, "ollama/deepseek-v4-pro:cloud"; got != want {
		t.Errorf("model = %q, want %q (provider/model form, bare ModelName not m.ID)", got, want)
	}
	if got, want := parsed.Provider.Ollama.Options.BaseURL, "http://localhost:11434/v1"; got != want {
		t.Errorf("baseURL = %q, want %q (OllamaURL() already includes /v1; format string must not double-suffix)", got, want)
	}
	if strings.Contains(parsed.Provider.Ollama.Options.BaseURL, "/v1/v1") {
		t.Errorf("baseURL has doubled /v1/v1 suffix: %s", parsed.Provider.Ollama.Options.BaseURL)
	}
}

// Claude must treat non-sentinel claude/* models (e.g. claude/opus) as
// native-provider launches: clear the inherited ANTHROPIC_* gateway
// vars so the claude subscription wins, and pass --model <ModelName> so
// claude picks the named model. Routing a non-sentinel claude/* model
// through the ollama gateway would silently fail — claude would ask
// ollama for a model the gateway doesn't recognize. This is the
// regression test for the provider-keyed dispatch change in claude.go.
func TestClaudeNativeProviderNamed(t *testing.T) {
	d := ByName("claude")
	if d == nil {
		t.Fatal("claude driver not registered")
	}
	lc := d.Build(namedNativeModel("claude", "opus"), false, directGateway())
	if len(lc.Args) != 2 || lc.Args[0] != "--model" || lc.Args[1] != "opus" {
		t.Errorf("args = %v, want [--model opus]", lc.Args)
	}
	if len(lc.Env) != 0 {
		t.Errorf("env = %v, want none (subscription wins)", lc.Env)
	}
	for _, k := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"} {
		if !slices.Contains(lc.ClearEnv, k) {
			t.Errorf("ClearEnv = %v, want it to include %q", lc.ClearEnv, k)
		}
	}
}

// Copilot must treat non-sentinel copilot/* models as native-provider
// launches: clear the COPILOT_* gateway vars so the copilot subscription
// wins. Copilot's CLI does not accept --model, so the launch is bare
// (no env, no args) regardless of whether the model name is the
// sentinel or a real name.
func TestCopilotNativeProviderNamed(t *testing.T) {
	d := ByName("copilot")
	if d == nil {
		t.Fatal("copilot driver not registered")
	}
	lc := d.Build(namedNativeModel("copilot", "auto"), false, directGateway())
	if len(lc.Args) != 0 {
		t.Errorf("args = %v, want none", lc.Args)
	}
	if len(lc.Env) != 0 {
		t.Errorf("env = %v, want none (subscription wins)", lc.Env)
	}
	for _, k := range []string{"COPILOT_PROVIDER_BASE_URL", "COPILOT_PROVIDER_API_KEY", "COPILOT_PROVIDER_WIRE_API", "COPILOT_MODEL"} {
		if !slices.Contains(lc.ClearEnv, k) {
			t.Errorf("ClearEnv = %v, want it to include %q", lc.ClearEnv, k)
		}
	}
}

// TestBuildLaunchCmdNativeSkipsResume pins the !m.Native defense-in-depth
// guard at the package level. The wrapper-level TestBuildLaunchNativeSkipsResume
// in cmd/wt covers the same contract end-to-end, but if a refactor drops the
// `!m.Native` clause from BuildLaunchCmd itself, this test is the only
// direct pin — without it the bug could regress silently if the wrapper test
// is later deleted or rewritten as a perceived duplicate. Without this guard,
// resuming a session would restore the session's stored model and silently
// override the user's "native" choice (for claude, routing a gateway model at
// the real Anthropic API).
func TestBuildLaunchCmdNativeSkipsResume(t *testing.T) {
	cmd, err := BuildLaunchCmd("claude", nativeModel("claude"), "/tmp/repo", false,
		&session.Session{ID: "abc-123", MTime: time.Now()}, nil, nil)
	if err != nil {
		t.Fatalf("BuildLaunchCmd: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	if strings.Contains(got, "--resume") || strings.Contains(got, "--session") {
		t.Errorf("args = %q, native model must not resume", got)
	}
}

// TestBuildLaunchCmdNamedNativeSkipsResume pins the broadened §5 resume-skip
// predicate for a *named* native-provider model (claude/opus, ModelName
// "opus", Native true), not just the claude/native sentinel. The sentinel
// tests (TestBuildLaunchCmdNativeSkipsResume and its cmd/wt + tui wrappers)
// would still pass if the predicate regressed from !m.Native back to
// !m.IsNative() — which only checks ModelName == "native" — so this test is
// the guard that a named native model, which the old IsNative() would have
// missed, is not resumed.
func TestBuildLaunchCmdNamedNativeSkipsResume(t *testing.T) {
	cmd, err := BuildLaunchCmd("claude", namedNativeModel("claude", "opus"), "/tmp/repo", false,
		&session.Session{ID: "abc-123", MTime: time.Now()}, nil, nil)
	if err != nil {
		t.Fatalf("BuildLaunchCmd: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	if strings.Contains(got, "--resume") || strings.Contains(got, "--session") {
		t.Errorf("args = %q, named native model must not resume", got)
	}
}

// TestBuildLaunchCmdResumeNonNative pins the inverse: a non-native model with
// a non-nil session must append --resume. Together with
// TestBuildLaunchCmdNativeSkipsResume, this locks both halves of the
// `sess != nil && !m.Native` guard so neither clause can drift.
func TestBuildLaunchCmdResumeNonNative(t *testing.T) {
	cmd, err := BuildLaunchCmd("claude",
		config.Model{ID: "ollama/kimi-k2.7-code:cloud", ModelName: "kimi-k2.7-code:cloud"},
		"/tmp/repo", false,
		&session.Session{ID: "abc-123", MTime: time.Now()}, nil, nil)
	if err != nil {
		t.Fatalf("BuildLaunchCmd: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	if !strings.Contains(got, "--resume abc-123") {
		t.Errorf("args = %q, want --resume abc-123", got)
	}
}

// TestResumerCapability asserts which drivers implement the Resumer
// optional capability. claude and opencode support session resume; the
// other agents do not. This is the contract that lets shared code stop
// switching on the agent name.
func TestResumerCapability(t *testing.T) {
	cases := []struct {
		agent     string
		resumable bool
	}{
		{"claude", true},
		{"opencode", true},
		{"codex", false},
		{"copilot", false},
		{"pi", false},
		{"agy", false},
		{"shell", false},
	}
	for _, c := range cases {
		d := ByName(c.agent)
		if d == nil {
			t.Fatalf("unknown agent: %s", c.agent)
		}
		_, got := d.(Resumer)
		if got != c.resumable {
			t.Errorf("agent %q Resumer = %v, want %v", c.agent, got, c.resumable)
		}
	}
}

// TestSeederCapability asserts which drivers implement the Seeder
// optional capability. claude and copilot create instruction pointer
// files; other agents do not.
func TestSeederCapability(t *testing.T) {
	cases := []struct {
		agent string
		wants bool
	}{
		{"claude", true},
		{"copilot", true},
		{"codex", false},
		{"opencode", false},
		{"pi", false},
		{"agy", false},
		{"shell", false},
	}
	for _, c := range cases {
		d := ByName(c.agent)
		if d == nil {
			t.Fatalf("unknown agent: %s", c.agent)
		}
		_, got := d.(Seeder)
		if got != c.wants {
			t.Errorf("agent %q Seeder = %v, want %v", c.agent, got, c.wants)
		}
	}
}

// TestOllamaURLerCapability asserts which drivers implement the
// OllamaURLer optional capability. claude, copilot, codex, and opencode
// route non-native models through a local Ollama gateway; other agents
// do not.
func TestOllamaURLerCapability(t *testing.T) {
	cases := []struct {
		agent string
		wants bool
	}{
		{"claude", true},
		{"copilot", true},
		{"codex", true},
		{"opencode", true},
		{"pi", false},
		{"agy", false},
		{"shell", false},
	}
	for _, c := range cases {
		d := ByName(c.agent)
		if d == nil {
			t.Fatalf("unknown agent: %s", c.agent)
		}
		_, got := d.(OllamaURLer)
		if got != c.wants {
			t.Errorf("agent %q OllamaURLer = %v, want %v", c.agent, got, c.wants)
		}
	}
}

// TestListEntries verifies the neutral agent-list builder merges configured
// agents and registered drivers, deduplicates, classifies commands, and
// reports issues. This is the shared helper that replaces near-identical
// list construction in the TUI and configeditor.
func TestListEntries(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"ollama"}},
			{Name: "definitely-not-installed", SupportedProviders: []string{"ollama"}},
		},
	}

	// Stub the installed check so the test is deterministic regardless of
	// which binaries happen to be on the host's PATH.
	installed := func(bin string) bool { return bin == "claude" }

	entries := ListEntries(cfg, installed)
	byName := map[string]AgentListEntry{}
	for _, e := range entries {
		if _, ok := byName[e.Name]; ok {
			t.Errorf("duplicate entry for %q", e.Name)
		}
		byName[e.Name] = e
	}

	if _, ok := byName["claude"]; !ok {
		t.Fatal("missing claude entry")
	}
	if !byName["claude"].Configured {
		t.Error("claude should be configured")
	}
	if !byName["claude"].Installed {
		t.Error("claude should be installed (stubbed installed check)")
	}
	if byName["claude"].Issue != "" {
		t.Errorf("claude issue = %q, want empty", byName["claude"].Issue)
	}

	if e, ok := byName["shell"]; !ok {
		t.Fatal("missing shell command entry")
	} else {
		if !e.Command {
			t.Error("shell should be marked as a command")
		}
		if !e.Installed {
			t.Error("commands are always installed")
		}
		if e.Issue != "" {
			t.Errorf("shell issue = %q, want empty", e.Issue)
		}
	}

	if e, ok := byName["definitely-not-installed"]; !ok {
		t.Fatal("missing definitely-not-installed entry")
	} else {
		if !strings.Contains(e.Issue, "not installed") {
			t.Errorf("definitely-not-installed issue = %q, want not installed", e.Issue)
		}
	}

	if e, ok := byName["opencode"]; !ok {
		t.Fatal("missing opencode entry")
	} else {
		if !strings.Contains(e.Issue, "not configured") {
			t.Errorf("opencode issue = %q, want not configured", e.Issue)
		}
	}

	// Sorted alphabetically.
	for i := 1; i < len(entries); i++ {
		if entries[i].Name < entries[i-1].Name {
			t.Errorf("entries not sorted: %q before %q", entries[i-1].Name, entries[i].Name)
		}
	}
}

// TestIssueFor verifies the single-agent launch blocker: configured+installed
// is launchable, a missing config or binary produces the right message, and
// commands are always launchable. This is the shared helper behind both the
// picker rows and the TUI's pinned-agent path, so a regression here would
// either hide a real problem or block a valid launch.
func TestIssueFor(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"ollama"}},
			{Name: "definitely-not-installed", SupportedProviders: []string{"ollama"}},
		},
	}
	installed := func(bin string) bool { return bin == "claude" }

	if got := IssueFor(cfg, "claude", installed); got != "" {
		t.Errorf("IssueFor(claude) = %q, want \"\" (configured + installed)", got)
	}
	if got := IssueFor(cfg, "definitely-not-installed", installed); !strings.Contains(got, "not installed") {
		t.Errorf("IssueFor(definitely-not-installed) = %q, want to mention not installed", got)
	}
	if got := IssueFor(cfg, "opencode", installed); !strings.Contains(got, "not configured") {
		t.Errorf("IssueFor(opencode) = %q, want to mention not configured", got)
	}
	if got := IssueFor(cfg, "shell", installed); got != "" {
		t.Errorf("IssueFor(shell) = %q, want \"\" (command)", got)
	}
}
