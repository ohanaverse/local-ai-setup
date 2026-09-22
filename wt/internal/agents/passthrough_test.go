package agents

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// IsConfigured is the single source of truth the launch paths use to decide
// whether an agent has a model catalog to resolve against. Commands are
// always configured (no model layer to catalog); a model-driven agent needs
// a config.toml entry, and a nil cfg (a caller working around a config load
// failure) is never configured.
func TestIsConfigured(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"ollama"}},
		},
	}
	cases := []struct {
		name  string
		cfg   *config.Config
		agent string
		want  bool
	}{
		{"command is always configured", cfg, "shell", true},
		{"command is configured even with nil cfg", nil, "shell", true},
		{"configured model-driven agent", cfg, "claude", true},
		{"unconfigured model-driven agent", cfg, "opencode", false},
		{"nil cfg is never configured for a real agent", nil, "opencode", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsConfigured(c.cfg, c.agent); got != c.want {
				t.Errorf("IsConfigured(cfg, %q) = %v, want %v", c.agent, got, c.want)
			}
		})
	}
}

// Every model-driven driver, given the passthrough sentinel (Native: true,
// ModelName: "native"), must add no --model flag and no model-routing env
// var. This is what makes the unconfigured-agent passthrough (issue #147)
// launch every agent bare; a new driver that skips this convention would
// silently route through a stale or empty gateway URL instead of the
// agent's own default config or subscription.
func TestPassthroughModelProducesNoModelOverride(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "copilot", "opencode", "pi", "agy"} {
		t.Run(agent, func(t *testing.T) {
			d := ByName(agent)
			if d == nil {
				t.Fatalf("%s driver not registered", agent)
			}
			lc := d.Build(passthroughModel(), false, Route{})
			for _, a := range lc.Args {
				if a == "--model" {
					t.Errorf("%s: Args = %v, want no --model flag for the passthrough sentinel", agent, lc.Args)
				}
			}
			if len(lc.Env) != 0 {
				t.Errorf("%s: Env = %v, want no env vars for the passthrough sentinel", agent, lc.Env)
			}
		})
	}
}

// BuildPassthroughCmd must build a bare launch command per driver: correct
// binary resolution, the requested worktree as cmd.Dir, no --model flag, and
// extraArgs appended. Skipped per-agent when the binary is not on PATH
// (mirrors the skip pattern cmd/wt/launch_test.go and internal/agents'
// resume tests already use for real-binary launcher tests).
func TestBuildPassthroughCmd(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "copilot", "opencode", "pi", "agy"} {
		t.Run(agent, func(t *testing.T) {
			if !Installed(agent) {
				t.Skipf("%s not installed on PATH; skipping launcher test", agent)
			}
			cmd, err := BuildPassthroughCmd(agent, "/tmp/repo", false, nil)
			if err != nil {
				t.Fatalf("BuildPassthroughCmd: %v", err)
			}
			if cmd.Dir != "/tmp/repo" {
				t.Errorf("cmd.Dir = %q, want /tmp/repo", cmd.Dir)
			}
			for _, a := range cmd.Args {
				if a == "--model" {
					t.Errorf("args %v contain --model, want a bare launch", cmd.Args)
				}
			}
		})
	}
}

// BuildPassthroughCmd must thread yolo and extraArgs through to the built
// command, and must not panic on the nil cfg/session it passes to
// BuildLaunchCmd. Uses a test-only driver (bash-backed, always on PATH)
// instead of a real agent so the assertion is deterministic in any
// environment, unlike TestBuildPassthroughCmd's per-driver skips.
func TestBuildPassthroughCmdUsesSentinelAndExtraArgs(t *testing.T) {
	register("_test_passthrough", func() Driver { return regularTestDriver{} })
	t.Cleanup(func() { delete(registry, "_test_passthrough") })

	cmd, err := BuildPassthroughCmd("_test_passthrough", "/tmp", true, []string{"--foo"})
	if err != nil {
		t.Fatalf("BuildPassthroughCmd: %v", err)
	}
	if len(cmd.Args) == 0 || cmd.Args[len(cmd.Args)-1] != "--foo" {
		t.Errorf("cmd.Args = %v, want --foo appended", cmd.Args)
	}
}

// An unknown agent name must return the same "unknown agent" error
// BuildLaunchCmd already produces — BuildPassthroughCmd is a thin wrapper,
// not a second source of validation.
func TestBuildPassthroughCmdUnknownAgent(t *testing.T) {
	_, err := BuildPassthroughCmd("not-an-agent", "/tmp", false, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Errorf("err = %v, want it to contain \"unknown agent\"", err)
	}
}
