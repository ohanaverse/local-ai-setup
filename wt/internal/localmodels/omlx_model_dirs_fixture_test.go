package localmodels

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type omlxDirsCase struct {
	Name     string   `json:"name"`
	ModelDir string   `json:"model_dir"`
	Files    []string `json:"files"`
	Expect   []string `json:"expect"`
}

// TestScanOmlxModelsMatchesSharedFixture builds each tree in
// docs/contracts/omlx-model-dirs.sample.json under a temp dir and checks that
// scanOmlxModels lists the expected names. modelman's omlx_model_dirs is
// tested against the same file (modelman/tests/contracts), so the two scans
// cannot drift apart; a regression here means wt lists a model omlx does not
// serve (or hides one it does), and the picker offers a start that fails.
func TestScanOmlxModelsMatchesSharedFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/contracts/omlx-model-dirs.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Cases []omlxDirsCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Cases) == 0 {
		t.Fatal("fixture has no cases")
	}
	for _, c := range fx.Cases {
		t.Run(c.Name, func(t *testing.T) {
			root := t.TempDir()
			for _, f := range c.Files {
				p := filepath.Join(root, filepath.FromSlash(f))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := scanOmlxModels(filepath.Join(root, filepath.FromSlash(c.ModelDir)))
			if err != nil {
				t.Fatalf("case %q: %v", c.Name, err)
			}
			if len(got) == 0 && len(c.Expect) == 0 {
				return
			}
			if !slices.Equal(got, c.Expect) {
				t.Errorf("case %q: got %v, want %v", c.Name, got, c.Expect)
			}
		})
	}
}
