package agents

import (
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/session"
)

// dshModel returns an ollama-provider model with a distinct ID/ModelName so
// the bare-model contract (m.ModelName, never m.ID) can be asserted: passing
// m.ID would forward "ollama/deepseek-v4-pro:cloud" to ollama launch, which
// expects the bare name.
func dshModel() config.Model {
	return config.Model{ID: "ollama/deepseek-v4-pro:cloud", ProviderID: "ollama", ModelName: "deepseek-v4-pro:cloud", Location: config.LocationCloud}
}

// TestDshWebUIBuild verifies dsh-webui builds `ollama launch dsh --model
// <bare name>` with no profile selector and no yolo flag. The browser mode is
// dsh's default, so no --profile is needed; a stray --profile or --yolo would
// be rejected by ollama launch (which defines only --model/--config/--restore/
// --yes). yolo is passed as true to prove it is ignored.
func TestDshWebUIBuild(t *testing.T) {
	d := ByName("dsh-webui")
	if d == nil {
		t.Fatal("dsh-webui driver not registered")
	}
	lc := d.Build(dshModel(), true)
	want := []string{"launch", "dsh", "--model", "deepseek-v4-pro:cloud"}
	if !slices.Equal(lc.Args, want) {
		t.Errorf("dsh-webui args = %v, want %v", lc.Args, want)
	}
	if lc.Bin != "ollama" {
		t.Errorf("dsh-webui bin = %q, want ollama", lc.Bin)
	}
}

// TestDshTuiBuild verifies dsh-tui builds `ollama launch dsh -- --profile tui`.
// The --profile selector is a dsh flag, not an ollama launch flag, so it must
// follow the `--` separator; passing it as a launcher flag would be rejected
// with "unknown flag: --profile".
func TestDshTuiBuild(t *testing.T) {
	d := ByName("dsh-tui")
	if d == nil {
		t.Fatal("dsh-tui driver not registered")
	}
	lc := d.Build(config.Model{}, false)
	want := []string{"launch", "dsh", "--", "--profile", "tui"}
	if !slices.Equal(lc.Args, want) {
		t.Errorf("dsh-tui args = %v, want %v", lc.Args, want)
	}
}

// TestDshHeadlessBuild verifies dsh-headless builds
// `ollama launch dsh -- --profile headless`. Same contract as dsh-tui: the
// profile selector must follow the `--` separator.
func TestDshHeadlessBuild(t *testing.T) {
	d := ByName("dsh-headless")
	if d == nil {
		t.Fatal("dsh-headless driver not registered")
	}
	lc := d.Build(config.Model{}, false)
	want := []string{"launch", "dsh", "--", "--profile", "headless"}
	if !slices.Equal(lc.Args, want) {
		t.Errorf("dsh-headless args = %v, want %v", lc.Args, want)
	}
}

// TestDshPassthrough verifies passthrough args land after the `--` separator
// so ollama forwards them to dsh rather than parsing them as launcher flags.
// `dsh-webui -- --port 3081` must become
// `ollama launch dsh --model <name> -- --port 3081`; without the re-inserted
// `--`, ollama launch would reject --port as an unknown flag. This exercises
// the full BuildLaunchCmd path (ArgSetter dispatch), not just Build.
func TestDshPassthrough(t *testing.T) {
	cmd, err := BuildLaunchCmd("dsh-webui", dshModel(), "/tmp", false, nil, nil, []string{"--port", "3081"})
	if err != nil {
		if !strings.Contains(err.Error(), "not installed") {
			t.Fatalf("BuildLaunchCmd: %v", err)
		}
		return
	}
	// cmd.Args[0] is the resolved ollama path; the driver-built args follow.
	want := []string{"launch", "dsh", "--model", "deepseek-v4-pro:cloud", "--", "--port", "3081"}
	if !strings.HasSuffix(cmd.Args[0], "ollama") {
		t.Errorf("cmd.Args[0] = %q, want it to resolve to ollama", cmd.Args[0])
	}
	if !slices.Equal(cmd.Args[1:], want) {
		t.Errorf("cmd.Args[1:] = %v, want %v", cmd.Args[1:], want)
	}
}

// TestDshTuiPassthrough verifies the command modes append passthrough args
// after the profile selector (already past the `--` separator), so
// `dsh-tui -- --foo` becomes `ollama launch dsh -- --profile tui --foo`.
func TestDshTuiPassthrough(t *testing.T) {
	cmd, err := BuildLaunchCmd("dsh-tui", config.Model{}, "/tmp", false, nil, nil, []string{"--foo"})
	if err != nil {
		if !strings.Contains(err.Error(), "not installed") {
			t.Fatalf("BuildLaunchCmd: %v", err)
		}
		return
	}
	// cmd.Args[0] is the resolved ollama path; the driver-built args follow.
	want := []string{"launch", "dsh", "--", "--profile", "tui", "--foo"}
	if !strings.HasSuffix(cmd.Args[0], "ollama") {
		t.Errorf("cmd.Args[0] = %q, want it to resolve to ollama", cmd.Args[0])
	}
	if !slices.Equal(cmd.Args[1:], want) {
		t.Errorf("cmd.Args[1:] = %v, want %v", cmd.Args[1:], want)
	}
}

// TestDshNoYolo verifies no dsh driver exposes a yolo flag. ollama launch has
// no skip-permissions flag, so YoloFlag must return "" and Build must ignore
// the yolo bool; a non-empty flag would produce `ollama --yolo launch ...`,
// which ollama rejects.
func TestDshNoYolo(t *testing.T) {
	for _, name := range []string{"dsh-webui", "dsh-tui", "dsh-headless"} {
		d := ByName(name)
		if d == nil {
			t.Fatalf("%s driver not registered", name)
		}
		if got := d.YoloFlag(); got != "" {
			t.Errorf("%s YoloFlag() = %q, want \"\"", name, got)
		}
	}
}

// TestDshNoResume verifies BuildLaunchCmd never appends --resume/--session for
// a dsh agent, even when a prior session exists. dsh starts a fresh session
// each launch; a resume flag would be rejected by ollama launch (which has no
// --resume/--session flag).
func TestDshNoResume(t *testing.T) {
	sess := &session.Session{ID: "abc123"}
	for _, name := range []string{"dsh-webui", "dsh-tui", "dsh-headless"} {
		cmd, err := BuildLaunchCmd(name, dshModel(), "/tmp", false, sess, nil, nil)
		if err != nil {
			if !strings.Contains(err.Error(), "not installed") {
				t.Fatalf("BuildLaunchCmd(%s): %v", name, err)
			}
			continue
		}
		for _, a := range cmd.Args {
			if a == "--resume" || a == "--session" {
				t.Errorf("%s args contain %q, want no resume flag", name, a)
			}
		}
	}
}
