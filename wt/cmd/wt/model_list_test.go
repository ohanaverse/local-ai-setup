package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// modelListConfig is a registry with the row shapes a real one has: a long
// cloud id, a running omlx model, a missing one, an ollama model with a size,
// and a pairing.
func modelListConfig() *config.Config {
	local := func(id string) config.Provider {
		return config.Provider{ID: id, Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}
	}
	return &config.Config{
		Providers: []config.Provider{
			local("ollama"), local("omlx"), local("mlx_lm_server"),
			{ID: "openrouter", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "api_key"}},
		},
		Models: []config.Model{
			{ID: "openrouter/anthropic--claude-sonnet-4.5-thinking", Family: "sonnet", ProviderID: "openrouter", ModelName: "anthropic/claude-sonnet-4.5-thinking", Tags: []string{"code"}},
			{ID: "omlx/Qwen3.8-27B-Instruct-MLX-6bit", Family: "qwen3.8", ProviderID: "omlx", ModelName: "Qwen3.8-27B-Instruct-MLX-6bit"},
			{ID: "omlx/Gone-4bit", Family: "qwen3.8", ProviderID: "omlx", ModelName: "Gone-4bit"},
			{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b"},
			{ID: "mlx_lm_server/Qwen3.8-27B+draft-Qwen3.8-4B", Family: "qwen3.8", ProviderID: "mlx_lm_server", ModelName: "Qwen3.8-27B+draft-Qwen3.8-4B",
				Fetch: config.ModelArtifact{Repo: "mlx-community/Qwen3.8-27B"}, Draft: config.ModelArtifact{Repo: "mlx-community/Qwen3.8-4B"}},
		},
	}
}

func modelListSnapshot() localmodels.Snapshot {
	return localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK, "ollama": localmodels.StatusOK, "mlx_lm_server": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/Qwen3.8-27B-Instruct-MLX-6bit", ModelName: "Qwen3.8-27B-Instruct-MLX-6bit", Artifact: "Qwen3.8-27B-Instruct-MLX-6bit",
				Registered: true, ArtifactKnown: true, Running: true, Path: "/Users/dev/.omlx/models/Qwen3.8-27B-Instruct-MLX-6bit"},
			{ProviderID: "omlx", ModelID: "omlx/Gone-4bit", ModelName: "Gone-4bit", Registered: true, ArtifactKnown: true},
			{ProviderID: "ollama", ModelID: "ollama/gemma4:9b", ModelName: "gemma4:9b", Artifact: "gemma4:9b", Registered: true, ArtifactKnown: true, Size: 5_800_000_000},
			{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/Qwen3.8-27B+draft-Qwen3.8-4B", ModelName: "Qwen3.8-27B+draft-Qwen3.8-4B", Registered: true},
			{ProviderID: "ollama", ModelID: "ollama/qwen3:8b", ModelName: "qwen3:8b", Artifact: "qwen3:8b", ArtifactKnown: true, Size: 5_200_000_000},
		},
	}
}

// TestModelListFitsEightyColumns is the spec's measurement: at 80 columns
// the listing has no borders, no line is wider than the terminal, no id is
// cut (an id too long for the MODEL column gets a line of its own, above its
// cells), the five columns that say what a row is are all there with SIZE,
// and PATH, which does not fit, is left to --json and named on stderr. A
// bordered table of these rows is over 150 columns wide and wraps into an
// unreadable grid.
func TestModelListFitsEightyColumns(t *testing.T) {
	stubProbeInventory(t, modelListSnapshot())
	var out, errOut bytes.Buffer
	if err := runModelList(&out, &errOut, modelListConfig(), false, 80); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"MODEL                                   FAMILY   LOC    STATUS   RUNNING    SIZE",
		"ollama/gemma4:9b                        gemma4   local  ok                5.8 GB",
		"mlx_lm_server/Qwen3.8-27B+draft-Qwen3.8-4B",
		"                                        qwen3.8  local  -                      -",
		"omlx/Gone-4bit                          qwen3.8  local  missing                -",
		"omlx/Qwen3.8-27B-Instruct-MLX-6bit      qwen3.8  local  ok       run           -",
		"openrouter/anthropic--claude-sonnet-4.5-thinking",
		"                                        sonnet   cloud  ok                     -",
		"ollama/qwen3:8b                         -        local  new               5.2 GB",
		"",
	}, "\n")
	if got := out.String(); got != want {
		t.Errorf("listing at 80 columns =\n%s\nwant\n%s", got, want)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("line is %d columns wide, want at most 80: %q", w, line)
		}
		if strings.HasSuffix(line, " ") {
			t.Errorf("line ends in a space: %q", line)
		}
	}
	for _, m := range modelListConfig().Models {
		if !strings.Contains(out.String(), m.ID) {
			t.Errorf("model id %s is missing or cut", m.ID)
		}
	}
	if got := errOut.String(); got != "(PATH not shown at this width; `wt model list --json` has every column)\n" {
		t.Errorf("stderr = %q, want the note naming the dropped column", got)
	}
}

// TestModelListDropsSizeBeforeItCutsAnId verifies that at 60 columns SIZE
// goes too, because keeping it would leave MODEL under 20 columns, and that
// at 40 the five kept columns stay whole with every id on a line of its own.
// The id is what the user copies into `wt model edit` or `wt -M`, so it is
// moved, never cut.
func TestModelListDropsSizeBeforeItCutsAnId(t *testing.T) {
	stubProbeInventory(t, modelListSnapshot())
	var out, errOut bytes.Buffer
	if err := runModelList(&out, &errOut, modelListConfig(), false, 60); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "SIZE") || !strings.Contains(errOut.String(), "SIZE and PATH not shown") {
		t.Errorf("at 60 columns: out =\n%s\nstderr = %q; want no SIZE column and a note", out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "ollama/gemma4:9b            gemma4   local  ok\n") {
		t.Errorf("at 60 columns a short id is not beside its cells:\n%s", out.String())
	}
	out.Reset()
	if err := runModelList(&out, &errOut, modelListConfig(), false, 40); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out.String(), "\n")
	if lines[0] != "MODEL   FAMILY   LOC    STATUS   RUNNING" {
		t.Errorf("header at 40 columns = %q", lines[0])
	}
	if !strings.Contains(out.String(), "openrouter/anthropic--claude-sonnet-4.5-thinking\n        sonnet   cloud  ok\n") {
		t.Errorf("at 40 columns the long id is not on a line of its own above its cells:\n%s", out.String())
	}
	for _, line := range lines {
		if !strings.Contains(line, "/") && lipgloss.Width(line) > 40 {
			t.Errorf("a line of cells is %d columns wide at 40: %q", lipgloss.Width(line), line)
		}
	}
}

// TestModelListWithoutAWidthLimit verifies that into a pipe (width 0) every
// column is printed and every model is one line, so `wt model list | grep`
// works and the path is there to cut out.
func TestModelListWithoutAWidthLimit(t *testing.T) {
	stubProbeInventory(t, modelListSnapshot())
	var out, errOut bytes.Buffer
	if err := runModelList(&out, &errOut, modelListConfig(), false, 0); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 7 {
		t.Fatalf("%d lines, want a header and six rows:\n%s", len(lines), out.String())
	}
	if !strings.HasSuffix(lines[0], "SIZE  PATH") {
		t.Errorf("header = %q, want SIZE and PATH", lines[0])
	}
	if !strings.HasSuffix(lines[4], "run           -  /Users/dev/.omlx/models/Qwen3.8-27B-Instruct-MLX-6bit") || !strings.HasPrefix(lines[4], "omlx/Qwen3.8-27B-Instruct-MLX-6bit  ") {
		t.Errorf("running omlx row = %q, want its path last", lines[4])
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want nothing when no column is dropped", errOut.String())
	}
}

// TestModelListJSON verifies the --json document: every row with its status
// and running state, size_bytes and path as values or null, an empty tags
// array (never null), and each probed provider's status. Scripts read this;
// the text table drops columns to fit.
func TestModelListJSON(t *testing.T) {
	stubProbeInventory(t, modelListSnapshot())
	var out, errOut bytes.Buffer
	if err := runModelList(&out, &errOut, modelListConfig(), true, 80); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Registry string `json:"registry"`
		Models   []struct {
			ID         string   `json:"id"`
			Tags       []string `json:"tags"`
			Registered bool     `json:"registered"`
			Status     string   `json:"status"`
			Running    string   `json:"running"`
			SizeBytes  *int64   `json:"size_bytes"`
			Path       *string  `json:"path"`
			Target     string   `json:"target"`
			Draft      string   `json:"draft"`
		} `json:"models"`
		Providers map[string]string `json:"providers"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if doc.Registry != config.RegistryPath() || len(doc.Models) != 6 || doc.Providers["omlx"] != "ok" {
		t.Fatalf("doc = %+v", doc)
	}
	byID := map[string]int{}
	for i, m := range doc.Models {
		byID[m.ID] = i
	}
	omlx := doc.Models[byID["omlx/Qwen3.8-27B-Instruct-MLX-6bit"]]
	if omlx.Running != "run" || omlx.Path == nil || *omlx.Path != "/Users/dev/.omlx/models/Qwen3.8-27B-Instruct-MLX-6bit" || omlx.SizeBytes != nil {
		t.Errorf("omlx row = %+v, want running, its path, and a null size", omlx)
	}
	found := doc.Models[byID["ollama/qwen3:8b"]]
	if found.Registered || found.Status != "new" || found.SizeBytes == nil || *found.SizeBytes != 5_200_000_000 || found.Path != nil {
		t.Errorf("discovered row = %+v, want unregistered, new, its size and a null path", found)
	}
	pair := doc.Models[byID["mlx_lm_server/Qwen3.8-27B+draft-Qwen3.8-4B"]]
	if pair.Status != "-" || pair.Target != "mlx-community/Qwen3.8-27B" || pair.Draft != "mlx-community/Qwen3.8-4B" {
		t.Errorf("pairing = %+v, want status - with its target and draft", pair)
	}
	if !strings.Contains(out.String(), `"tags": []`) || strings.Contains(out.String(), `"tags": null`) {
		t.Errorf("a row with no tags must print an empty array:\n%s", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want nothing in JSON mode", errOut.String())
	}
}

// TestModelListWithNothingToList verifies an empty registry on a machine
// with no local model prints one line that says so, not a lone header row.
func TestModelListWithNothingToList(t *testing.T) {
	stubProbeInventory(t, localmodels.Snapshot{})
	var out, errOut bytes.Buffer
	if err := runModelList(&out, &errOut, &config.Config{}, false, 80); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "no models: ") || strings.Contains(out.String(), "MODEL") {
		t.Errorf("out = %q, want a one-line note and no table", out.String())
	}
}

// TestPlainTableAlignsBothKindsOfColumn verifies the renderer the usage and
// model listings share: text columns are padded on the right, number columns
// on the left, and no line ends in a space. A table that padded numbers on
// the right would misalign every SIZE and SPEND value.
func TestPlainTableAlignsBothKindsOfColumn(t *testing.T) {
	cols := []plainColumn{{head: "KEY"}, {head: "TEXT"}, {head: "NUM", right: true}, {head: "LAST"}}
	got := renderPlainTable(cols, [][]string{{"a", "long text", "7", "x"}, {"bb", "t", "1234", ""}}, 0)
	want := "KEY  TEXT        NUM  LAST\n" +
		"a    long text     7  x\n" +
		"bb   t          1234"
	if got != want {
		t.Errorf("table =\n%s\nwant\n%s", got, want)
	}
	if key, rest := plainTableWidth(cols, [][]string{{"a", "long text", "7", "x"}}); key != 3 || rest != 2+9+2+3+2+4 {
		t.Errorf("plainTableWidth = %d, %d; want 3 and 22", key, rest)
	}
}

// TestModelListKeepsSizeWhenEveryIdIsShort verifies SIZE is dropped only
// when the table really does not fit: with ids all shorter than the 20
// columns MODEL is otherwise guaranteed, the six columns fit a terminal
// narrower than 20 plus the rest, and dropping SIZE there (with a note that
// it is not shown) would take a column off a table that had room for it.
func TestModelListKeepsSizeWhenEveryIdIsShort(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models: []config.Model{
			{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b"},
		},
	}
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/gemma4:9b", ModelName: "gemma4:9b", Artifact: "gemma4:9b", Registered: true, ArtifactKnown: true, Size: 5_800_000_000},
		},
	})
	var wide bytes.Buffer
	if err := runModelList(&wide, &bytes.Buffer{}, cfg, false, 0); err != nil {
		t.Fatal(err)
	}
	// The six columns without PATH: the full line less its last cell.
	header := strings.Split(wide.String(), "\n")[0]
	need := lipgloss.Width(strings.TrimRight(strings.TrimSuffix(header, "PATH"), " "))
	if !strings.HasSuffix(header, "SIZE  PATH") || need < 40 {
		t.Fatalf("fixture: unexpected full header %q", header)
	}
	var out, errOut bytes.Buffer
	if err := runModelList(&out, &errOut, cfg, false, need); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "SIZE") || !strings.Contains(out.String(), "5.8 GB") {
		t.Errorf("at %d columns, exactly what six columns need:\n%s\nwant SIZE kept", need, out.String())
	}
	if got := errOut.String(); got != "(PATH not shown at this width; `wt model list --json` has every column)\n" {
		t.Errorf("stderr = %q, want only PATH named as dropped", got)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if w := lipgloss.Width(line); w > need {
			t.Errorf("line is %d columns wide, want at most %d: %q", w, need, line)
		}
	}
	out.Reset()
	errOut.Reset()
	if err := runModelList(&out, &errOut, cfg, false, need-1); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "SIZE") || !strings.Contains(errOut.String(), "SIZE and PATH not shown") {
		t.Errorf("one column narrower:\n%s\nstderr %q; want SIZE dropped and named", out.String(), errOut.String())
	}
}

// TestModelListCommandGate runs `wt model list` through cobra to pin which
// error stops it. A registry with a gap in it (the config loads, Validate
// fails) is what the list exists to show, so it must still print; only a
// registry that could not be loaded at all refuses. Swapping the gate to the
// validation error would take away the one command for diagnosing that gap.
func TestModelListCommandGate(t *testing.T) {
	stubProbeInventory(t, modelListSnapshot())
	run := func(a *app) (string, error) {
		c := modelListCmd(a)
		var out bytes.Buffer
		c.SetOut(&out)
		c.SetErr(&bytes.Buffer{})
		c.SetArgs(nil)
		err := c.Execute()
		return out.String(), err
	}
	out, err := run(&app{cfg: modelListConfig(), cfgErr: errors.New(`model "corp/x": provider_id "corp" names no provider row`)})
	if err != nil || !strings.Contains(out, "ollama/gemma4:9b") {
		t.Errorf("a config that loads but does not validate: err = %v, out =\n%s\nwant the listing", err, out)
	}
	loadErr := errors.New("registry.toml: toml: line 3: expected a value")
	out, err = run(&app{cfg: &config.Config{}, cfgErr: loadErr, loadErr: loadErr})
	if err == nil || !strings.Contains(err.Error(), "expected a value") || out != "" {
		t.Errorf("a config that did not load: err = %v, out = %q; want the load error and no listing", err, out)
	}
}
