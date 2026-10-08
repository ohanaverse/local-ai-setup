package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
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
				return "model registry not found at " + registry + " — seed it with `wt model init`"
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
			never: []string{"wt config", "wt model init", "modelman"},
		},
		{
			name: "unparseable registry",
			setup: func(t *testing.T, registry, _ string) {
				write(t, registry, "models = [unclosed\n")
			},
			errHas: func(registry string) string { return "parse " + registry + ": toml: " },
			hint:   "fix that file by hand",
			never:  []string{"wt config", "wt model init", "modelman"},
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
			never:   []string{"wt config", "wt model init", "modelman"},
		},
		{
			name: "unparseable config.toml",
			setup: func(t *testing.T, registry, cfgFile string) {
				write(t, registry, goodRegistry)
				write(t, cfgFile, "default_tag = [unclosed\n")
			},
			errHas: func(string) string { return "parse config: toml: " },
			hint:   "run `wt config` to repair",
			never:  []string{"wt model init", "modelman", "by hand", "the link"},
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
		if n := strings.Count(got, "wt model init"); n != 1 {
			t.Errorf("message %q names `wt model init` %d times, want exactly once", got, n)
		}
	}
	if strings.Contains(strings.TrimSuffix(got, "\n"), "\n") {
		t.Errorf("message %q spans more than one line", got)
	}
}

// TestLitellmLoadErrorNamesTheRepairThatWorks runs `wt litellm sync` and
// `wt litellm status` (the cfgGuard path `on`, `off` and `set` share) in a
// home broken each way wt's configuration can fail to load, and pins the
// repair the refusal names. Both used to end every such refusal with "run
// `wt config` to repair": for a missing registry that contradicted the
// `wt model init` the same line already named, and for a registry link that
// leads nowhere it sent the user to an editor that cannot touch the link
// (#291). Only a config.toml problem is `wt config`'s to repair.
func TestLitellmLoadErrorNamesTheRepairThatWorks(t *testing.T) {
	for _, c := range loadErrorCases() {
		for _, args := range [][]string{{"litellm", "sync"}, {"litellm", "status"}, {"litellm", "on"}} {
			t.Run(c.name+"/"+strings.Join(args, " "), func(t *testing.T) {
				registry, _ := brokenHome(t, c)
				root := rootCmd()
				var out, errOut bytes.Buffer
				root.SetOut(&out)
				root.SetErr(&errOut)
				root.SetArgs(args)
				err := root.Execute()
				if err == nil {
					t.Fatalf("wt %v succeeded (stdout %q); want it to refuse on the load error", args, out.String())
				}
				c.check(t, err.Error(), "config error: ", " (%s)", "", registry)
			})
		}
	}
}

// TestStatsFamilyNoteNamesTheRepairThatWorks pins the repair in the note
// `wt stats --family` prints when the configuration did not load. The note
// has to agree with what every refusing command says for the same error: it
// used to tell a user with no registry to run `modelman migrate`, right
// after quoting an error that says `wt model init` (#291).
func TestStatsFamilyNoteNamesTheRepairThatWorks(t *testing.T) {
	for _, c := range loadErrorCases() {
		t.Run(c.name, func(t *testing.T) {
			registry, _ := brokenHome(t, c)
			a, err := newApp()
			if err != nil {
				t.Fatalf("newApp(): %v", err)
			}
			if a.loadErr == nil {
				t.Fatal("newApp() loaded a broken home without a load error")
			}
			_, stderr := runStats(t, a, "--family", "gemma4")
			const spendNote = "wt: spend unavailable: querySpend not stubbed in this test\n"
			note, ok := strings.CutPrefix(stderr, spendNote)
			if !ok {
				t.Fatalf("stderr = %q, want TestMain's spend note first", stderr)
			}
			c.check(t, note, "wt: wt's configuration did not load (", "; %s",
				"), so --family matched each id's provider prefix\n", registry)
		})
	}
}

// TestModelInitNamesTheRepairForARegistryItCannotWrite pins what
// `wt model init` says about a registry it will not write: one with a
// top-level key wt does not know, one that does not parse, one it cannot
// read at all (a directory of that name), and one whose path it cannot
// examine (a file where the directory should be). All are registry problems
// only a hand edit repairs, so the refusal says so instead of leaving the
// user to guess (or to try `wt config`, which edits another file) — and it
// says what every other command says about the same registry.
func TestModelInitNamesTheRepairForARegistryItCannotWrite(t *testing.T) {
	const isDir, parentIsFile = "\x00a directory", "\x00parent is a file"
	for name, body := range map[string]string{
		"unknown top-level key":   "providers = []\nmodels = []\nextra = 1\n",
		"unparseable":             "models = [unclosed\n",
		"unreadable":              isDir,
		"path cannot be examined": parentIsFile,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			withCleanConfigEnv(t, home)
			litellmEnv(t, "model_list: []\n")
			registry := filepath.Join(home, ".config", "local-ai", "registry.toml")
			if err := os.MkdirAll(filepath.Dir(filepath.Dir(registry)), 0o755); err != nil {
				t.Fatal(err)
			}
			if body == parentIsFile {
				if err := os.WriteFile(filepath.Dir(registry), []byte("not a directory\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(filepath.Dir(registry), 0o755); err != nil {
				t.Fatal(err)
			}
			switch body {
			case parentIsFile:
			case isDir:
				if err := os.Mkdir(registry, 0o755); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(registry, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			stubSeedEnv(t, config.SeedEnv{OnPath: onPath("ollama")})
			synced := stubRouteSync(t, "")
			err := runModelInit(&bytes.Buffer{}, &bytes.Buffer{}, false)
			if err == nil {
				t.Fatal("wt model init wrote a registry it cannot read")
			}
			got := err.Error()
			if !strings.Contains(got, registry) || !strings.HasSuffix(got, "(fix that file by hand)") {
				t.Errorf("err = %q, want it to name %s and end with the hand-edit hint", got, registry)
			}
			if strings.Contains(got, "wt config") {
				t.Errorf("err = %q names `wt config`, which cannot edit the registry", got)
			}
			if *synced != 0 {
				t.Errorf("route syncs after the refusal: %d, want none", *synced)
			}
			switch body {
			case parentIsFile:
				if b, _ := os.ReadFile(filepath.Dir(registry)); string(b) != "not a directory\n" {
					t.Errorf("the file in the registry directory's place after the refusal: %q, want it untouched", b)
				}
			case isDir:
				if info, err := os.Lstat(registry); err != nil || !info.IsDir() {
					t.Errorf("registry path after the refusal: %v (err %v), want the directory left alone", info, err)
				}
			default:
				if b, _ := os.ReadFile(registry); string(b) != body {
					t.Errorf("registry.toml after the refusal:\n%s\nwant it untouched", b)
				}
			}
		})
	}
}

// TestRegistryRowErrorNamesTheRegistry runs the commands that refuse on a
// validation error against a registry whose rows are wrong, and pins that the
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
		for _, args := range [][]string{{"start", "omlx/m"}, {"stop", "omlx/m"}, {"smoke", "omlx/m"}, {"-A", "claude", "-M", "omlx/m"}} {
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
				root := rootCmd()
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
