package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadErrorCase is one way wt's configuration fails to load, and the repair
// the user has to be sent to for it. `wt config` edits config.toml and
// nothing else, so it is the repair for a config.toml problem only (#291).
type loadErrorCase struct {
	name string
	// setup breaks the throwaway home; registry and cfgFile are the paths
	// wt reads there.
	setup func(t *testing.T, registry, cfgFile string)
	// errText is config.Load's whole error. When the text is the TOML
	// parser's, errHas is set instead and names the part wt words itself.
	errText func(registry string) string
	errHas  func(registry string) string
	// hint is the repair every command appends, "" when the error names
	// the repair itself. never lists the repairs that do not work here.
	hint  string
	never []string
}

const goodRegistry = "providers = []\nmodels = []\n"

func loadErrorCases() []loadErrorCase {
	write := func(t *testing.T, path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return []loadErrorCase{
		{
			name:  "missing registry",
			setup: func(t *testing.T, registry, cfgFile string) {},
			errText: func(registry string) string {
				return "model registry not found at " + registry + " — seed it with `wt-model model init`"
			},
			never: []string{"wt config", "modelman"},
		},
		{
			name: "dangling registry link",
			setup: func(t *testing.T, registry, _ string) {
				if err := os.MkdirAll(filepath.Dir(registry), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(danglingTarget(registry), registry); err != nil {
					t.Fatal(err)
				}
			},
			errText: func(registry string) string {
				return "registry link is broken: " + registry + " is a symlink to " + danglingTarget(registry) +
					", which does not exist"
			},
			hint:  "fix the link or move it aside",
			never: []string{"wt config", "wt-model model init", "modelman"},
		},
		{
			name: "unparseable registry",
			setup: func(t *testing.T, registry, _ string) {
				write(t, registry, "models = [unclosed\n")
			},
			errHas: func(registry string) string { return "parse " + registry + ": toml: " },
			hint:   "fix that file by hand",
			never:  []string{"wt config", "wt-model model init", "modelman"},
		},
		{
			// A regular file where the registry's directory should be: the
			// path cannot even be examined, and that is not "missing".
			name: "registry directory is a file",
			setup: func(t *testing.T, registry, _ string) {
				write(t, filepath.Dir(registry), "not a directory\n")
			},
			errText: func(registry string) string { return "lstat " + registry + ": not a directory" },
			hint:    "fix that file by hand",
			never:   []string{"wt config", "wt-model model init", "modelman"},
		},
		{
			name: "unparseable config.toml",
			setup: func(t *testing.T, registry, cfgFile string) {
				write(t, registry, goodRegistry)
				write(t, cfgFile, "default_tag = [unclosed\n")
			},
			errHas: func(string) string { return "parse config: toml: " },
			hint:   "run `wt config` to repair",
			never:  []string{"wt-model model init", "modelman", "by hand", "the link"},
		},
	}
}

func danglingTarget(registry string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(registry)), "unmounted", "registry.toml")
}

// brokenHome builds a throwaway home broken the way c describes and returns
// the registry and config.toml paths wt reads in it. config.yaml and the
// proxy restart are pointed at a scratch file and a no-op, so a command
// that wrongly got past its load-error guard still could not reach LiteLLM.
func brokenHome(t *testing.T, c loadErrorCase) (registry, cfgFile string) {
	t.Helper()
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	litellmEnv(t, "model_list: []\n")
	registry = filepath.Join(home, ".config", "local-ai", "registry.toml")
	cfgFile = filepath.Join(home, ".config", "agent-wt", "config.toml")
	c.setup(t, registry, cfgFile)
	return registry, cfgFile
}

// check reports how got, one whole message, departs from what c promises:
// prefix, the load error, the hint laid out by hintFmt (nothing at all when
// the error names its own repair), suffix.
func (c loadErrorCase) check(t *testing.T, got, prefix, hintFmt, suffix, registry string) {
	t.Helper()
	tail := suffix
	if c.hint != "" {
		tail = fmt.Sprintf(hintFmt, c.hint) + suffix
	}
	if c.errText != nil {
		if want := prefix + c.errText(registry) + tail; got != want {
			t.Errorf("message =\n  %q\nwant\n  %q", got, want)
		}
	} else if has := c.errHas(registry); !strings.HasPrefix(got, prefix+has) || !strings.HasSuffix(got, tail) {
		t.Errorf("message =\n  %q\nwant it to start %q and end %q", got, prefix+has, tail)
	}
	for _, bad := range c.never {
		if strings.Contains(got, bad) {
			t.Errorf("message %q names %q, which does not repair this", got, bad)
		}
	}
	if c.name == "missing registry" {
		if n := strings.Count(got, "wt-model model init"); n != 1 {
			t.Errorf("message %q names `wt-model model init` %d times, want exactly once", got, n)
		}
	}
	if strings.Contains(strings.TrimSuffix(got, "\n"), "\n") {
		t.Errorf("message %q spans more than one line", got)
	}
}

// TestStatsFamilyNoteNamesTheRepairThatWorks pins the repair in the note
// `wt stats --family` prints when the configuration did not load (none for a
// registry simply absent at the default path: a session-only machine). The note
// has to agree with what every refusing command says for the same error: it
// used to tell a user with no registry to run a command of the retired
// modelman, right after quoting an error that says `wt-model model init` (#291).
func TestStatsFamilyNoteNamesTheRepairThatWorks(t *testing.T) {
	for _, c := range loadErrorCases() {
		t.Run(c.name, func(t *testing.T) {
			registry, _ := brokenHome(t, c)
			a, err := newApp()
			if err != nil {
				t.Fatalf("newApp(): %v", err)
			}
			if hostLoadErrOf(a) == nil {
				t.Fatal("newApp() loaded a broken home without a load error")
			}
			_, stderr := runStats(t, a, "--family", "gemma4")
			// The spend line comes first: no model server is configured, or
			// (an unparseable config.toml) the file that would name one did
			// not load. Then the registry's note, which the host registry
			// (hostConfig) gives the families when there is no server.
			first, note, ok := strings.Cut(stderr, "\n")
			if !ok || !strings.HasPrefix(first, "wt: spend unavailable: ") || !strings.HasSuffix(first, "; spend is not shown") {
				t.Fatalf("stderr = %q, want the spend line first", stderr)
			}
			if wantNC := c.name != "unparseable config.toml"; strings.Contains(first, "no model server is configured") != wantNC {
				t.Errorf("case %q: spend line %q, want not-configured = %v", c.name, first, wantNC)
			}
			if c.name == "missing registry" {
				// No registry at the default path is a session-only
				// machine: there is nothing to repair, so no note
				// (TestStatsFamilyNoNoteWithoutARegistry).
				if note != "" {
					t.Errorf("missing registry: note = %q, want none", note)
				}
				return
			}
			c.check(t, note, "wt: wt's configuration did not load (", "; %s",
				"), so --family matched each id's provider prefix\n", registry)
		})
	}
}

// TestRegistryRowErrorNamesTheRegistry runs the commands that refuse on a
// validation error against a registry whose rows are wrong (since the flip
// those are the host's `wt stop <provider>` and `wt stop --all`: start, a
// model's stop, smoke and a launch read no registry), and pins that the
// refusal sends the user to registry.toml. The rows parse, so this is not a
// load error, and it used to fall through to "run `wt config` to repair" —
// for a model with no model_name right after the same line had said to edit
// registry.toml. `wt config` edits config.toml and cannot repair a provider
// or model row (#291).
func TestRegistryRowErrorNamesTheRegistry(t *testing.T) {
	const provider = "[[providers]]\nid = \"omlx\"\nlocation = \"local\"\n\n"
	for name, c := range map[string]struct{ body, want string }{
		"model without model_name": {
			provider + "[[models]]\nid = \"omlx/m\"\nprovider_id = \"omlx\"\n",
			`model "omlx/m": model_name is required`,
		},
		"model with an unknown provider": {
			provider + "[[models]]\nid = \"ghost/m\"\nprovider_id = \"ghost\"\nmodel_name = \"m\"\n",
			`model "ghost/m": unknown provider "ghost"`,
		},
		"duplicate provider id": {provider + provider, `duplicate provider id "omlx"`},
		"model with no location": {
			"[[providers]]\nid = \"omlx\"\n\n[[models]]\nid = \"omlx/m\"\nprovider_id = \"omlx\"\nmodel_name = \"m\"\n",
			`model "omlx/m": no location on model or provider "omlx"`,
		},
	} {
		for _, args := range [][]string{{"stop", "omlx"}, {"stop", "--all"}} {
			t.Run(name+"/"+strings.Join(args, " "), func(t *testing.T) {
				home := t.TempDir()
				withCleanConfigEnv(t, home)
				litellmEnv(t, "model_list: []\n")
				registry := filepath.Join(home, ".config", "local-ai", "registry.toml")
				if err := os.MkdirAll(filepath.Dir(registry), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(registry, []byte(c.body), 0o644); err != nil {
					t.Fatal(err)
				}
				root := rootCmdWith(testHooks())
				var out, errOut bytes.Buffer
				root.SetOut(&out)
				root.SetErr(&errOut)
				root.SetArgs(args)
				err := root.Execute()
				if err == nil {
					t.Fatalf("wt %v ran on an invalid registry (stdout %q)", args, out.String())
				}
				want := "config error: " + c.want + " (fix the entry in " + registry + ")"
				if got := err.Error(); got != want {
					t.Errorf("wt %v:\n  %q\nwant\n  %q", args, got, want)
				}
			})
		}
	}
}
