package profiles

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// writeSlotFile points the config home at a temp directory and writes body
// as its slot-profiles.toml.
func writeSlotFile(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SlotPath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSlotPathSitsBesideProfilesToml checks that slot-profiles.toml is
// resolved in the same directory as profiles.toml. If it regresses, wt reads
// the file from somewhere the user did not write it and shows no profile rows.
func TestSlotPathSitsBesideProfilesToml(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	want := filepath.Join(dir, "agent-wt", "slot-profiles.toml")
	if got := SlotPath(); got != want {
		t.Errorf("SlotPath() = %q, want %q", got, want)
	}
	if filepath.Dir(SlotPath()) != filepath.Dir(Path()) {
		t.Errorf("SlotPath() dir = %q, want the directory of Path() %q", filepath.Dir(SlotPath()), filepath.Dir(Path()))
	}
}

// TestLoadSlotsMissingFileIsEmpty checks that a machine with no
// slot-profiles.toml loads an empty store and no error. If it regresses,
// every wt launch on a machine that never wrote the file prints a warning.
func TestLoadSlotsMissingFileIsEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store, err := LoadSlots()
	if err != nil {
		t.Fatalf("LoadSlots() error = %v, want nil", err)
	}
	if len(store.Profiles) != 0 {
		t.Errorf("LoadSlots() profiles = %v, want none", store.Profiles)
	}
}

// TestLoadSlotsReadsEveryField checks that each documented field of a
// [[profile]] entry reaches the store, and that entries keep file order. If
// it regresses, a field the user wrote is dropped without a word, or the
// picker lists profiles in an order the file does not have.
func TestLoadSlotsReadsEveryField(t *testing.T) {
	writeSlotFile(t, `
[[profile]]
name    = "second-in-sort-order"
agent   = "claude"
default = "sonnet"

[profile.slots]
opus   = "openrouter/vendor/big"
sonnet = "ollama/mid:cloud"

[profile.env]
MAX_THINKING_TOKENS = "4096"

[profile.config_content.modelOverrides]
"claude-sonnet-4-6" = "ollama/other:cloud"

[[profile]]
name    = "first-in-sort-order"
agent   = "pi"
default = "main"
args    = ["--flag", "value"]
wrapper = { binary = "little-coder", args_template = ["--pi-args", "{{args}}"] }

[profile.slots]
main = "omlx/local-model"
`)
	store, err := LoadSlots()
	if err != nil {
		t.Fatalf("LoadSlots() error = %v, want nil", err)
	}
	want := []SlotProfile{
		{
			Name:    "second-in-sort-order",
			Agent:   "claude",
			Default: "sonnet",
			Slots:   map[string]string{"opus": "openrouter/vendor/big", "sonnet": "ollama/mid:cloud"},
			Env:     map[string]string{"MAX_THINKING_TOKENS": "4096"},
			ConfigContent: map[string]any{
				"modelOverrides": map[string]any{"claude-sonnet-4-6": "ollama/other:cloud"},
			},
		},
		{
			Name:    "first-in-sort-order",
			Agent:   "pi",
			Default: "main",
			Slots:   map[string]string{"main": "omlx/local-model"},
			Args:    []string{"--flag", "value"},
			Wrapper: &WrapperSpec{Binary: "little-coder", ArgsTemplate: []string{"--pi-args", "{{args}}"}},
		},
	}
	if !reflect.DeepEqual(store.Profiles, want) {
		t.Errorf("LoadSlots() profiles =\n%#v\nwant\n%#v", store.Profiles, want)
	}
}

// TestLoadSlotsParseErrorIsReturned checks that a file that is not TOML
// comes back as an error naming the file, not as an empty store. If it
// regresses, a typo makes every profile row vanish with no warning line.
func TestLoadSlotsParseErrorIsReturned(t *testing.T) {
	writeSlotFile(t, "[[profile]\nname = \"x\"\n")
	store, err := LoadSlots()
	if err == nil {
		t.Fatal("LoadSlots() error = nil, want a parse error")
	}
	if !strings.HasPrefix(err.Error(), "parse slot-profiles.toml: ") {
		t.Errorf("LoadSlots() error = %q, want the prefix %q", err, "parse slot-profiles.toml: ")
	}
	if len(store.Profiles) != 0 {
		t.Errorf("LoadSlots() profiles = %v, want none beside an error", store.Profiles)
	}
}

// TestParseSlots checks what counts as a file that parses. If it regresses,
// a file with a wrongly typed value either stops every profile that used to
// load, or loads with the value quietly missing.
func TestParseSlots(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    int // number of profiles
		wantErr bool
	}{
		{name: "empty file", body: "", want: 0},
		{name: "comments only", body: "# nothing yet\n", want: 0},
		{name: "one bare profile", body: "[[profile]]\nname = \"a\"\n", want: 1},
		{name: "two profiles", body: "[[profile]]\nname = \"a\"\n[[profile]]\nname = \"b\"\n", want: 2},
		{name: "an unknown key does not stop the load", body: "[[profile]]\nname = \"a\"\ncolour = \"red\"\n", want: 1},
		{name: "a [[profiles]] table is another file's shape", body: "[[profiles]]\nagent = \"claude\"\n", want: 0},
		{name: "syntax error", body: "[[profile]\n", wantErr: true},
		{name: "a slot id is not a string", body: "[[profile]]\n[profile.slots]\nopus = 3\n", wantErr: true},
		{name: "an env value is not a string", body: "[[profile]]\n[profile.env]\nMAX_THINKING_TOKENS = 4096\n", wantErr: true},
		{name: "args is not a list", body: "[[profile]]\nargs = \"--flag\"\n", wantErr: true},
		{name: "a key set twice", body: "[[profile]]\nname = \"a\"\nname = \"b\"\n", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, err := ParseSlots([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseSlots() error = nil, want a parse error; store = %#v", store)
				}
				if !strings.HasPrefix(err.Error(), "parse slot-profiles.toml: ") {
					t.Errorf("ParseSlots() error = %q, want the prefix %q", err, "parse slot-profiles.toml: ")
				}
				if len(store.Profiles) != 0 {
					t.Errorf("ParseSlots() profiles = %v, want none beside an error", store.Profiles)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSlots() error = %v, want nil", err)
			}
			if len(store.Profiles) != tc.want {
				t.Errorf("ParseSlots() gave %d profiles, want %d", len(store.Profiles), tc.want)
			}
		})
	}
}

// TestParseSlotsReportsUnknownKeys checks that a key the schema does not name
// is reported, once, while the file still loads. If it regresses, a file
// written with [[profiles]] for [[profile]] gives no profile and no word of
// why, and a misspelt table (envs for env) launches without what it held.
func TestParseSlotsReportsUnknownKeys(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
		n    int // number of profiles
	}{
		{name: "nothing unknown", body: "[[profile]]\nname = \"a\"\n[profile.slots]\nopus = \"x\"\n", n: 1},
		{name: "the other file's array, once however many entries and keys it has",
			body: "[[profiles]]\nname = \"a\"\n[profiles.slots]\nopus = \"x\"\n[[profiles]]\nname = \"b\"\n",
			want: []string{"profiles"}},
		{name: "a top-level key and a misspelt key in a profile",
			body: "top = 1\n[[profile]]\nname = \"a\"\ncolour = \"red\"\n",
			want: []string{"top", "profile.colour"}, n: 1},
		{name: "a misspelt table is one key, and the same key in two profiles is one line",
			body: "[[profile]]\nname = \"a\"\n[profile.envs]\nA = \"1\"\n[[profile]]\nname = \"b\"\n[profile.envs]\nB = \"2\"\n",
			want: []string{"profile.envs"}, n: 2},
		{name: "a misspelt wrapper key",
			body: "[[profile]]\nname = \"a\"\n[profile.wrapper]\nbinary = \"x\"\nargs = [\"y\"]\n",
			want: []string{"profile.wrapper.args"}, n: 1},
		{name: "config_content is free-form, at any depth",
			body: "[[profile]]\nname = \"a\"\n[profile.config_content]\nx = 1\n[profile.config_content.y]\nz = 2\n[profile.config_content.y.w]\nv = [1]\n",
			n:    1},
		{name: "env and slots take any key",
			body: "[[profile]]\nname = \"a\"\n[profile.env]\nANY = \"1\"\n[profile.slots]\nany = \"x\"\n",
			n:    1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, err := ParseSlots([]byte(tc.body))
			if err != nil {
				t.Fatalf("ParseSlots() error = %v, want nil: an unknown key never fails the load", err)
			}
			if !slices.Equal(store.Unknown, tc.want) {
				t.Errorf("ParseSlots() unknown keys = %q, want %q", store.Unknown, tc.want)
			}
			if len(store.Profiles) != tc.n {
				t.Errorf("ParseSlots() gave %d profiles, want %d", len(store.Profiles), tc.n)
			}
		})
	}
}

// TestSlotProfileID checks the row id a profile is listed and pinned under.
// If it regresses, `-M profile/<name>` no longer finds the row the picker
// shows.
func TestSlotProfileID(t *testing.T) {
	if got := (SlotProfile{Name: "mixed"}).ID(); got != "profile/mixed" {
		t.Errorf("ID() = %q, want %q", got, "profile/mixed")
	}
	if SlotIDPrefix != SlotReservedProvider+"/" {
		t.Errorf("SlotIDPrefix = %q, want SlotReservedProvider %q followed by a slash", SlotIDPrefix, SlotReservedProvider)
	}
}

// TestFilledOrder checks the order a profile's slots are listed in: the
// default first, then the rest in the driver's declared order. If it
// regresses, the picker's slot line and `wt profile show` list the slots in
// a random or misleading order, or show a slot the agent does not have.
func TestFilledOrder(t *testing.T) {
	declared := []string{"fable", "opus", "sonnet", "haiku"}
	tests := []struct {
		name     string
		profile  SlotProfile
		declared []string
		want     []string
	}{
		{
			name:     "default first, the rest in declared order",
			profile:  SlotProfile{Default: "sonnet", Slots: map[string]string{"haiku": "c", "sonnet": "b", "opus": "a"}},
			declared: declared,
			want:     []string{"sonnet", "opus", "haiku"},
		},
		{
			name:     "default is the first declared slot",
			profile:  SlotProfile{Default: "fable", Slots: map[string]string{"fable": "a", "haiku": "b"}},
			declared: declared,
			want:     []string{"fable", "haiku"},
		},
		{
			name:     "one slot",
			profile:  SlotProfile{Default: "opus", Slots: map[string]string{"opus": "a"}},
			declared: declared,
			want:     []string{"opus"},
		},
		{
			name:     "a default the profile does not fill is not listed",
			profile:  SlotProfile{Default: "opus", Slots: map[string]string{"haiku": "a", "sonnet": "b"}},
			declared: declared,
			want:     []string{"sonnet", "haiku"},
		},
		{
			name:     "an undeclared slot is not listed, as default or otherwise",
			profile:  SlotProfile{Default: "turbo", Slots: map[string]string{"turbo": "a", "extra": "b", "haiku": "c"}},
			declared: declared,
			want:     []string{"haiku"},
		},
		{
			name:     "no slots",
			profile:  SlotProfile{Default: "opus"},
			declared: declared,
			want:     nil,
		},
		{
			name:     "no declared slots",
			profile:  SlotProfile{Default: "opus", Slots: map[string]string{"opus": "a"}},
			declared: nil,
			want:     nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.profile.FilledOrder(tc.declared)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("FilledOrder() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMergeConfigContent checks that a profile's config_content merges by
// the rule automatic profiles use: tables key by key, a scalar or an array
// whole. If it regresses, a profile that sets one key of a settings table
// wipes the table's other keys, or its override is ignored.
func TestMergeConfigContent(t *testing.T) {
	dst := map[string]any{
		"env":     map[string]any{"KEEP": "1", "REPLACE": "old"},
		"model":   "old",
		"allowed": []any{"a"},
		"other":   "untouched",
	}
	src := map[string]any{
		"env":            map[string]any{"REPLACE": "new", "ADD": "2"},
		"model":          "new",
		"allowed":        []any{"b", "c"},
		"modelOverrides": map[string]any{"claude-sonnet-4-6": "ollama/other:cloud"},
	}
	MergeConfigContent(dst, src)
	want := map[string]any{
		"env":            map[string]any{"KEEP": "1", "REPLACE": "new", "ADD": "2"},
		"model":          "new",
		"allowed":        []any{"b", "c"},
		"other":          "untouched",
		"modelOverrides": map[string]any{"claude-sonnet-4-6": "ollama/other:cloud"},
	}
	if !reflect.DeepEqual(dst, want) {
		t.Errorf("MergeConfigContent() dst =\n%#v\nwant\n%#v", dst, want)
	}
	wantSrc := map[string]any{
		"env":            map[string]any{"REPLACE": "new", "ADD": "2"},
		"model":          "new",
		"allowed":        []any{"b", "c"},
		"modelOverrides": map[string]any{"claude-sonnet-4-6": "ollama/other:cloud"},
	}
	if !reflect.DeepEqual(src, wantSrc) {
		t.Errorf("MergeConfigContent() changed src to\n%#v\nwant it left as\n%#v", src, wantSrc)
	}
	// Merging nothing, from nil or into an empty object, is not an error.
	MergeConfigContent(dst, nil)
	if !reflect.DeepEqual(dst, want) {
		t.Errorf("MergeConfigContent(dst, nil) changed dst to %#v", dst)
	}
}

// fakeSlotDriver is the smallest thing that satisfies SlotCapable. It
// echoes its input, so the test below can read what a driver receives.
type fakeSlotDriver struct{}

func (fakeSlotDriver) SlotNames() []string       { return []string{"big", "small"} }
func (fakeSlotDriver) ReservedEnvKeys() []string { return []string{"FAKE_MODEL"} }
func (fakeSlotDriver) BuildSlots(in SlotBuildInput) (SlotCommand, error) {
	return SlotCommand{
		Bin:              in.Agent,
		Args:             []string{"--model", in.Default, "--big", in.Models["big"].ID},
		Env:              []string{"FAKE_URL=" + in.ProxyOrigin, "FAKE_KEY=" + in.ProxyKey},
		ClearEnv:         []string{"FAKE_MODEL"},
		ClearEnvPrefixes: []string{"FAKE_DEFAULT_"},
	}, nil
}

// TestSlotCapableCarriesAResolvedProfileToADriver checks the shape of the
// driver seam: one value holds the resolved slot models, the default slot,
// the proxy origin and key and the yolo flag, and the driver answers with a
// command. If it regresses, internal/agents can no longer implement slot
// support without this package importing it, which is an import cycle.
func TestSlotCapableCarriesAResolvedProfileToADriver(t *testing.T) {
	var d SlotCapable = fakeSlotDriver{}
	in := SlotBuildInput{
		SlotLaunch: SlotLaunch{
			Agent:         "fake",
			Models:        map[string]config.Model{"big": {ID: "ollama/big:cloud"}, "small": {ID: "ollama/big:cloud"}},
			Default:       "big",
			Env:           map[string]string{"X": "1"},
			ConfigContent: map[string]any{"k": "v"},
		},
		ProxyOrigin: "http://localhost:4000",
		ProxyKey:    "sk-test",
		Yolo:        true,
	}
	got, err := d.BuildSlots(in)
	if err != nil {
		t.Fatalf("BuildSlots() error = %v, want nil", err)
	}
	want := SlotCommand{
		Bin:              "fake",
		Args:             []string{"--model", "big", "--big", "ollama/big:cloud"},
		Env:              []string{"FAKE_URL=http://localhost:4000", "FAKE_KEY=sk-test"},
		ClearEnv:         []string{"FAKE_MODEL"},
		ClearEnvPrefixes: []string{"FAKE_DEFAULT_"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildSlots() = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(d.SlotNames(), []string{"big", "small"}) {
		t.Errorf("SlotNames() = %v, want [big small]", d.SlotNames())
	}
}
