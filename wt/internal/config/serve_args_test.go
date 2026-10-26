package config

import (
	"slices"
	"strings"
	"testing"
)

const serveArgsHead = `
[[providers]]
id = "mtplx"
location = "local"
[providers.auth]
type = "none"

[[providers]]
id = "rec"
location = "local"
[providers.auth]
type = "none"

[[models]]
id = "mtplx/first"
family = "qwen"
provider_id = "mtplx"
model_name = "Org/first"
tags = ["code"]
`

const serveArgsTail = `
[[models]]
id = "mtplx/second"
family = "qwen"
provider_id = "mtplx"
model_name = "Org/second"
serve_args = ["--profile", "stable"]
`

// TestLoadReadsServeArgsAndToleratesAMalformedOne verifies serve_args (#432)
// reads as the argv elements it holds, and that a value that is not an array
// of strings reads as absent with the record set and the registry still loads,
// as for fetch and draft: models are edited by hand, and a slip in a key that
// only a start reads must not stop every wt command. The row after the
// malformed one is read as written.
func TestLoadReadsServeArgsAndToleratesAMalformedOne(t *testing.T) {
	for _, c := range []struct {
		name      string
		body      string
		want      []string
		malformed bool
	}{
		{"absent", ``, nil, false},
		{"empty array", `serve_args = []`, nil, false},
		{"one flag and its value", `serve_args = ["--profile", "turbo"]`, []string{"--profile", "turbo"}, false},
		{"a lone flag", `serve_args = ["--verbose"]`, []string{"--verbose"}, false},
		{"a string", `serve_args = "--profile turbo"`, nil, true},
		{"a number", `serve_args = 7`, nil, true},
		{"a table", `serve_args = { profile = "turbo" }`, nil, true},
		{"an element that is not a string", `serve_args = ["--profile", 3]`, nil, true},
		{"a nested array", `serve_args = [["--profile"]]`, nil, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			writeRegistry(t, t.TempDir(), serveArgsHead+c.body+"\n"+serveArgsTail)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() = %v, want the registry to load", err)
			}
			if len(cfg.Models) != 2 {
				t.Fatalf("got %d models, want 2", len(cfg.Models))
			}
			first := cfg.Models[0]
			if first.ID != "mtplx/first" || len(first.Tags) != 1 {
				t.Errorf("the rest of the row changed: %+v", first)
			}
			if !slices.Equal(first.ServeArgs.Args, c.want) || first.ServeArgs.Malformed != c.malformed {
				t.Errorf("ServeArgs = %+v, want args %v, malformed %v", first.ServeArgs, c.want, c.malformed)
			}
			if got := slices.Contains(first.Malformed(), "serve_args is not an array of strings"); got != c.malformed {
				t.Errorf("Malformed() = %v, names the value: %v, want %v", first.Malformed(), got, c.malformed)
			}
			if got := cfg.Models[1].ServeArgs.Args; !slices.Equal(got, []string{"--profile", "stable"}) {
				t.Errorf("the next row's ServeArgs = %v, want [--profile stable]", got)
			}
		})
	}
}

// TestServeArgsProblems pins the one judge of serve_args (the loader, the
// writer, `wt model add|edit` and a start all ask it): usable on an mtplx
// model, refused for each flag wt sets itself, alone or as --flag=value, for
// an empty element, and for a provider that takes none (rec, a provider with
// no spec, an unknown id). The first problem is what the writer and the CLI
// report, so the phrases are what a user reads.
func TestServeArgsProblems(t *testing.T) {
	for name, tc := range map[string]struct {
		provider string
		args     []string
		want     []string
	}{
		"nothing":                             {"mtplx", nil, nil},
		"fine":                                {"mtplx", []string{"--profile", "turbo", "--verbose"}, nil},
		"a value that looks like a flag name": {"mtplx", []string{"--profile", "port"}, nil},
		"port":                                {"mtplx", []string{"--port", "9"}, []string{"serve_args names --port, which wt sets itself"}},
		"port with =":                         {"mtplx", []string{"--port=9"}, []string{"serve_args names --port, which wt sets itself"}},
		"model":                               {"mtplx", []string{"--model", "x"}, []string{"serve_args names --model, which wt sets itself"}},
		"model-id":                            {"mtplx", []string{"--model-id=x"}, []string{"serve_args names --model-id, which wt sets itself"}},
		"host":                                {"mtplx", []string{"--host", "0.0.0.0"}, []string{"serve_args names --host, which wt sets itself"}},
		"a longer flag is not an owned one":   {"mtplx", []string{"--port-range", "1"}, nil},
		"empty":                               {"mtplx", []string{"--ok", ""}, []string{"serve_args has an empty argument"}},
		"rec":                                 {"rec", []string{"--x"}, []string{"serve_args is not used by provider rec (only mtplx models take it)"}},
		"unknown provider":                    {"nope", []string{"--x"}, []string{"serve_args is not used by provider nope (only mtplx models take it)"}},
		"none for rec is ok":                  {"rec", nil, nil},
	} {
		t.Run(name, func(t *testing.T) {
			if got := ServeArgsProblems(tc.provider, tc.args); !slices.Equal(got, tc.want) {
				t.Errorf("ServeArgsProblems(%q, %v) = %q, want %q", tc.provider, tc.args, got, tc.want)
			}
		})
	}
}

// TestUpdateRegistryWritesAndRefusesServeArgs verifies the writer (#432): a
// well-formed serve_args is written, reads back through the loader and stays
// byte-stable when written again; clearing it removes the key; and a value
// the loader would tolerate is refused by name instead: not an array, a flag
// wt sets itself, a provider that takes none. A write that stored them would
// turn into a start that refuses to spawn.
func TestUpdateRegistryWritesAndRefusesServeArgs(t *testing.T) {
	path := scratchRegistry(t, serveArgsHead+"\n[[models]]\nid = \"rec/flint\"\nfamily = \"f\"\nprovider_id = \"rec\"\nmodel_name = \"flint\"\n")
	set := func(id string, v any) error {
		_, err := UpdateRegistry(func(d *RegistryDoc) error { return d.PatchModel(id, map[string]any{"serve_args": v}, nil) })
		return err
	}
	if err := set("mtplx/first", []string{"--profile", "turbo"}); err != nil {
		t.Fatalf("a valid serve_args was refused: %v", err)
	}
	// At its place in the row's schema order, after the tags and before any
	// table, as the rows around it have their keys; not ahead of the id.
	text := readFile(t, path)
	if at, tags, name := strings.Index(text, "serve_args"), strings.Index(text, "tags = ["), strings.Index(text, `model_name = "Org/first"`); at < 0 || at < name || at < tags {
		t.Fatalf("serve_args is not written after model_name and tags:\n%s", text)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Models[0].ServeArgs.Args; !slices.Equal(got, []string{"--profile", "turbo"}) {
		t.Errorf("read back %v", got)
	}
	before := readFile(t, path)
	if err := set("mtplx/first", []string{"--profile", "turbo"}); err != nil || readFile(t, path) != before {
		t.Errorf("writing the same serve_args again changed the file (err %v)", err)
	}

	for name, tc := range map[string]struct {
		id   string
		v    any
		want string
	}{
		"not an array":         {"mtplx/first", "--profile turbo", "serve_args must be an array of strings"},
		"wt's own flag":        {"mtplx/first", []string{"--port", "9"}, "serve_args names --port, which wt sets itself"},
		"a provider with none": {"rec/flint", []string{"--x"}, "serve_args is not used by provider rec"},
	} {
		err := set(tc.id, tc.v)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to say %q", name, err, tc.want)
		}
		if readFile(t, path) != before {
			t.Errorf("%s: a refused write changed the file", name)
		}
	}

	if _, err := UpdateRegistry(func(d *RegistryDoc) error { return d.PatchModel("mtplx/first", nil, []string{"serve_args"}) }); err != nil {
		t.Fatal(err)
	}
	if text := readFile(t, path); strings.Contains(text, "serve_args") {
		t.Errorf("clearing left the key behind:\n%s", text)
	}
}
