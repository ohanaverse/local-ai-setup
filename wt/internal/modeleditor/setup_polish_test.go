package modeleditor

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/editorkit"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelsetup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeview"
)

// TestSetupTabParseErrorSaysTheErrorOnce pins that a config.toml that does
// not parse is explained once, in the banner: the mode row says it is
// unknown and the pair row says it was not read, neither repeating the error.
func TestSetupTabParseErrorSaysTheErrorOnce(t *testing.T) {
	st := parseErrState()
	m := setupEditor(t, &setupMachine{states: []modelsetup.State{st}}, 120, 50)
	v := plain(m)
	if n := strings.Count(flat(v), flat(`expected value but found "=" instead`)); n != 1 {
		t.Errorf("the toml error appears %d times, want 1:\n%s", n, v)
	}
	for _, want := range []string{"unknown: config.toml is not readable", "not read"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "unset: choose one below") {
		t.Errorf("mode still says unset:\n%s", v)
	}
}

// TestSetupTabParseErrorPathIsAbbreviatedAndBreaksAtSlashes pins that the
// banner writes the config path with ~ and, when it is wider than the
// terminal, breaks it only after a slash: no segment is split when it fits.
func TestSetupTabParseErrorPathIsAbbreviatedAndBreaksAtSlashes(t *testing.T) {
	st := parseErrState()
	st.Host.Home = "/Users/dev"
	st.Config.Path = "/Users/dev/.config/agent-wt/some-long-directory-name/another-long-directory/config.toml"
	for _, size := range [][2]int{{40, 50}, {80, 50}, {120, 50}} {
		m := setupEditor(t, &setupMachine{states: []modelsetup.State{st}}, size[0], size[1])
		assertFits(t, "parse error", m.View(), size[0], size[1])
		v := plain(m)
		if strings.Contains(v, "/Users/dev") {
			t.Errorf("width %d: the home is not abbreviated:\n%s", size[0], v)
		}
		lines := strings.Split(v, "\n")
		for _, seg := range strings.Split(strings.TrimPrefix(editorkit.HomePath(st.Config.Path, "/Users/dev", ""), "~/"), "/") {
			found := false
			for _, l := range lines {
				if strings.Contains(l, seg) {
					found = true
				}
			}
			if len(seg)+1 <= size[0] && !found {
				t.Errorf("width %d: the segment %q is split across lines:\n%s", size[0], seg, v)
			}
		}
	}
}

// setupPhasesForFit builds every non-prompt and prompt phase of the Setup
// tab at a size, for the clipping tests.
func setupPhasesForFit() map[string]func(*testing.T, int, int) *model {
	long := modelsetup.Plan{}
	for i := 0; i < 9; i++ {
		long.Steps = append(long.Steps, modelsetup.Step{Kind: modelsetup.StepPlist,
			Detail: "write wt's LaunchAgent plist /Users/dev/Library/LaunchAgents/local.wt-model.serve.plist: /Users/dev/.local/bin/wt-model serve --listen 127.0.0.1:8787"})
	}
	offLocal := offMacState()
	offLocal.Resolution = modelmode.Resolution{Mode: modelmode.ModeLocal, Source: modelmode.SourceStored}
	return map[string]func(*testing.T, int, int) *model{
		"view": func(t *testing.T, w, h int) *model {
			return setupEditor(t, &setupMachine{states: []modelsetup.State{fullState()}}, w, h)
		},
		"edit": func(t *testing.T, w, h int) *model {
			return keys(t, setupEditor(t, &setupMachine{states: []modelsetup.State{noKeyState()}}, w, h), "down", "down")
		},
		"confirm": func(t *testing.T, w, h int) *model {
			return keys(t, setupEditor(t, &setupMachine{states: []modelsetup.State{fullState()}, plan: long}, w, h), "a")
		},
		"remote machine": func(t *testing.T, w, h int) *model {
			return setupEditor(t, &setupMachine{states: []modelsetup.State{remoteState()}}, w, h)
		},
		"remove agent": func(t *testing.T, w, h int) *model {
			return keys(t, setupEditor(t, &setupMachine{states: []modelsetup.State{fullState()}}, w, h), "left", "a")
		},
		"applying": func(t *testing.T, w, h int) *model {
			m := keys(t, setupEditor(t, &setupMachine{states: []modelsetup.State{fullState()}, plan: long}, w, h), "a")
			next, _ := m.Update(keyMsg("y"))
			next, _ = next.(*model).Update(keyMsg("q"))
			return next.(*model)
		},
		"parse error": func(t *testing.T, w, h int) *model {
			return keys(t, setupEditor(t, &setupMachine{states: []modelsetup.State{parseErrState()}}, w, h), "a")
		},
		"off macOS": func(t *testing.T, w, h int) *model {
			return setupEditor(t, &setupMachine{states: []modelsetup.State{offMacState()}}, w, h)
		},
		"off macOS local": func(t *testing.T, w, h int) *model {
			return setupEditor(t, &setupMachine{states: []modelsetup.State{offLocal}}, w, h)
		},
	}
}

// TestSetupTabClippedViewKeepsRadiosHintsAndWholeRows pins the clipped
// view of every phase at 40x12 and 80x24: the mode choice stays whole (both
// options) outside the prompts, the hints are the last line, no row is
// left as an orphan continuation under a marker or a blank, and nothing is
// wider or taller than the terminal.
func TestSetupTabClippedViewKeepsRadiosHintsAndWholeRows(t *testing.T) {
	radio := regexp.MustCompile(`\([x\- ]\) local.*\([x\- ]\) native`)
	for name, open := range setupPhasesForFit() {
		for _, size := range [][2]int{{40, 12}, {80, 24}} {
			m := open(t, size[0], size[1])
			assertFits(t, name, m.View(), size[0], size[1])
			if last := hintLine(m); !strings.Contains(last, "quit") {
				t.Errorf("%s at %dx%d: the last line is %q, want the hints", name, size[0], size[1], last)
			}
			if m.setup.phase == setupConfirm {
				continue
			}
			v := plain(m)
			if !radio.MatchString(strings.Join(strings.Fields(strings.ReplaceAll(v, "\n", " ")), " ")) {
				t.Errorf("%s at %dx%d: the mode choice is not whole:\n%s", name, size[0], size[1], v)
			}
			lines := strings.Split(v, "\n")
			for i, l := range lines {
				if strings.HasPrefix(l, strings.Repeat(" ", editorkit.LabelW)) && strings.TrimSpace(l) != "" && i > 0 {
					if prev := strings.TrimSpace(lines[i-1]); prev == "" || strings.Contains(prev, "not shown") {
						t.Errorf("%s at %dx%d: orphan continuation line %q after %q:\n%s", name, size[0], size[1], l, lines[i-1], v)
					}
				}
			}
		}
	}
}

// TestSetupTabDropsLowestPriorityRowsFirstAndMarksTheEnd pins the order in
// which a short terminal gives up status rows (gateway, litellm, agent, env,
// pair, token, url, problems, server, mode last), that the marker names how many
// rows went and stands after the last row kept, and that the rows kept are
// always whole.
func TestSetupTabDropsLowestPriorityRowsFirstAndMarksTheEnd(t *testing.T) {
	order := []string{"gateway", "litellm", "agent", "env", "pair", "token", "url", "problems", "server", "mode"}
	marker := regexp.MustCompile(`\.\.\. (\d+) status rows? not shown \.\.\.`)
	sawDrop := false
	for h := 10; h <= 40; h++ {
		m := setupEditor(t, &setupMachine{states: []modelsetup.State{fullState()}}, 80, h)
		assertFits(t, "view", m.View(), 80, h)
		lines := strings.Split(plain(m), "\n")
		present := map[string]int{}
		markerAt, markerN := -1, 0
		for i, l := range lines {
			if strings.HasPrefix(l, "choose") {
				break
			}
			for _, k := range order {
				if strings.HasPrefix(l, k+" ") {
					present[k] = i
				}
			}
			if g := marker.FindStringSubmatch(l); g != nil {
				markerAt = i
				markerN = int(g[1][0] - '0')
				if len(g[1]) > 1 {
					markerN = markerN*10 + int(g[1][1]-'0')
				}
			}
		}
		missing, keepSeen := 0, false
		for _, k := range order {
			if _, ok := present[k]; !ok {
				missing++
				if keepSeen {
					t.Errorf("height %d: %q is dropped though a lower-priority row is kept:\n%s", h, k, strings.Join(lines, "\n"))
				}
			} else {
				keepSeen = true
				if markerAt >= 0 && present[k] > markerAt {
					t.Errorf("height %d: row %q is after the marker", h, k)
				}
			}
		}
		if missing > 0 {
			sawDrop = true
		}
		if missing != markerN {
			t.Errorf("height %d: %d rows dropped, marker says %d:\n%s", h, missing, markerN, strings.Join(lines, "\n"))
		}
	}
	if !sawDrop {
		t.Error("no height dropped a row; the test measures nothing")
	}
}

// TestSetupTabCancelsAPlanWhenCOrRReadsAgain pins that c or r while the
// read before a plan is out says planning was cancelled and clears the
// stored snapshot, secrets included, at once.
func TestSetupTabCancelsAPlanWhenCOrRReadsAgain(t *testing.T) {
	for _, k := range []string{"c", "r"} {
		sm := &setupMachine{states: []modelsetup.State{noKeyState()}}
		m := setupEditor(t, sm, 120, 50)
		m = keys(t, m, "down", "down", "down", "sk-secret", "esc")
		next, _ := m.Update(keyMsg("a"))
		m = next.(*model)
		if m.setup.planWant.MasterKey == "" {
			t.Fatalf("%s: no snapshot taken at a", k)
		}
		next, _ = m.Update(keyMsg(k))
		m = next.(*model)
		if !strings.Contains(m.setup.status, "planning cancelled") {
			t.Errorf("%s: status = %q, want planning cancelled", k, m.setup.status)
		}
		if m.setup.planWant.MasterKey != "" || m.setup.planWant != (modelsetup.Desired{}) || m.setup.planGen != 0 {
			t.Errorf("%s: the plan snapshot was not cleared: %+v", k, m.setup.planWant)
		}
	}
}

// TestSetupTabNamesWhatACheckIsMissing pins that a server that was not
// checked says which of the url and the token is missing.
func TestSetupTabNamesWhatACheckIsMissing(t *testing.T) {
	url := remoteState()
	url.Config.Table.URL = "https://gpu.example:8787"
	tok := remoteState()
	tok.Token = modelsetup.TokenState{Path: "/Users/dev/t", Exists: true}
	none := remoteState()
	for _, c := range []struct {
		name string
		st   modelsetup.State
		want string
	}{
		{"url only", url, "not checked: no token"},
		{"token only", tok, "not checked: no url"},
		{"neither", none, "not checked: no url and no token"},
	} {
		got := modeview.ServerCheckLine(c.st.State).Text
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// TestSetupTabPromptsStayVisibleAtEverySize pins that each of the two
// default-No questions is on screen at every size, with nothing but the hint
// after it: a clipped question would leave the user answering blind.
func TestSetupTabPromptsStayVisibleAtEverySize(t *testing.T) {
	long := modelsetup.Plan{Plan: modelmode.Plan{Notes: []string{"the environment sets WT_MODEL_URL, which overrides what is stored here"}}}
	for i := 0; i < 9; i++ {
		long.Steps = append(long.Steps, modelsetup.Step{Kind: modelsetup.StepPlist,
			Detail: "write wt's LaunchAgent plist /Users/dev/Library/LaunchAgents/local.wt-model.serve.plist: /Users/dev/.local/bin/wt-model serve --listen 127.0.0.1:8787"})
	}
	cases := []struct {
		prompt string
		open   func(*testing.T, int, int) *model
	}{
		{"Apply this plan? [y/N]", func(t *testing.T, w, h int) *model {
			return keys(t, setupEditor(t, &setupMachine{states: []modelsetup.State{fullState()}, plan: long}, w, h), "a")
		}},
		{"Remove the LaunchAgent? [y/N]", func(t *testing.T, w, h int) *model {
			return keys(t, setupEditor(t, &setupMachine{states: []modelsetup.State{fullState()}}, w, h), "left", "a")
		}},
	}
	for _, c := range cases {
		for _, size := range tabSizes {
			m := c.open(t, size[0], size[1])
			assertFits(t, c.prompt, m.View(), size[0], size[1])
			lines := strings.Split(plain(m), "\n")
			at := -1
			for i, l := range lines {
				if strings.Contains(l, c.prompt) {
					at = i
				}
			}
			if at < 0 {
				t.Errorf("%q at %dx%d: the question is not shown:\n%s", c.prompt, size[0], size[1], plain(m))
				continue
			}
			for _, l := range lines[at+1 : len(lines)-1] {
				if strings.TrimSpace(l) != "" {
					t.Errorf("%q at %dx%d: %q follows the question", c.prompt, size[0], size[1], l)
				}
			}
		}
	}
}

// TestSetupTabCancelsAPlanInFlightWhenCOrRReadsAgain pins the later twin of
// TestSetupTabCancelsAPlanWhenCOrRReadsAgain: once the read before a plan
// landed and the plan itself is out, c or r still says planning was
// cancelled (the plan is dropped by its generation when it lands), and the
// dropped plan never reaches the confirm screen.
func TestSetupTabCancelsAPlanInFlightWhenCOrRReadsAgain(t *testing.T) {
	for _, k := range []string{"c", "r"} {
		sm := &setupMachine{states: []modelsetup.State{fullState()}, plan: okPlan()}
		m := setupEditor(t, sm, 120, 50)
		next, read := m.Update(keyMsg("a"))
		m = next.(*model)
		next, plan := m.Update(read())
		m = next.(*model)
		if plan == nil {
			t.Fatalf("%s: the read before the plan did not hand the plan to a command", k)
		}
		next, _ = m.Update(keyMsg(k))
		m = next.(*model)
		if !strings.Contains(m.setup.status, "planning cancelled") {
			t.Errorf("%s: status = %q, want planning cancelled", k, m.setup.status)
		}
		m = send(t, m, plan())
		if m.setup.phase == setupConfirm {
			t.Errorf("%s: the cancelled plan reached the confirm screen", k)
		}
	}
}
