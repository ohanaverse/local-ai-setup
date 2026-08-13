# Lesson 3: Migration from legacy config

## Concept Intro

The existing tool stores configuration in a bash-sourced file at
`~/.config/agent-wt/models.conf` and rotation position in two small state
files, `rotation-code.state` and `rotation-design.state`. The new Go tool uses
a richer TOML config (`config.toml` from lesson 2). To preserve the user's
existing rotation lists and current position, we add a **one-time migration**:
on first run, if `config.toml` doesn't exist but `models.conf` does, read the
legacy file, convert it to the new format, write `config.toml`, and record
that migration happened (so it doesn't run again).

The legacy format is bash:

```bash
CODE_MODELS=("native:copilot" "deepseek-v4-pro:cloud" "native:claude")
DESIGN_MODELS=("native:copilot" "deepseek-v4-pro:cloud")
NATIVE_CLAUDE="native:claude"
PROVIDER_OLLAMA_BASE_URL="http://localhost:11434"
```

We can't safely `source` bash from Go, so we parse it with a small hand-rolled
line parser. Since the format is simple and machine-generated, a regex that
extracts the quoted tokens from `NAME=(...)` lines is sufficient and robust
enough for the migration path (it only runs once, on data we don't fully
control but which follows this exact shape).

## New Syntax & Vocabulary

| Term | Meaning |
|---|---|
| `regexp.MustCompile` | Compiles a regex at startup (panics on bad pattern). |
| `re.FindAllStringSubmatch` | Returns all non-overlapping matches and their capture groups. |
| `os.IsNotExist` | Reports whether the error is a missing file. |
| `strconv.Atoi` | Parses a decimal integer. |
| `Migration` | A small struct recording whether migration ran / the source paths. |
| `atomic file write` | Write to a temp file then rename, so a crash never leaves a half-written config. |

## Worked Walkthrough

Create `internal/config/migrate.go`:

```go
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// legacy paths under ~/.config/agent-wt/
func legacyPaths() (modelsConf, stateDir string) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	dir := filepath.Join(base, "agent-wt")
	return filepath.Join(dir, "models.conf"), dir
}

// parseBashArray extracts the quoted tokens from `NAME=( "a" "b" ... )`.
var bashArrayRe = regexp.MustCompile(`([A-Z_]+)=\(([^)]*)\)`)

func parseBashArray(line string) (name string, vals []string) {
	m := bashArrayRe.FindStringSubmatch(line)
	if m == nil {
		return "", nil
	}
	name = m[1]
	quoted := regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(m[2], -1)
	for _, q := range quoted {
		vals = append(vals, q[1])
	}
	return name, vals
}

// Migrate converts a legacy models.conf into the new TOML Config, if the new
// config does not already exist. Returns whether it performed a migration.
func Migrate() (bool, error) {
	if _, err := os.Stat(Path()); err == nil {
		return false, nil // new config already exists
	} else if !os.IsNotExist(err) {
		return false, err
	}

	legacyFile, stateDir := legacyPaths()
	data, err := os.ReadFile(legacyFile)
	if os.IsNotExist(err) {
		return false, nil // nothing to migrate
	}
	if err != nil {
		return false, err
	}

	cfg := &Config{DefaultTag: "code"}
	baseURL := "http://localhost:11434"
	for _, line := range strings.Split(string(data), "\n") {
		name, vals := parseBashArray(line)
		switch name {
		case "CODE_MODELS":
			cfg.Models = append(cfg.Models, tagModels(vals, "code")...)
		case "DESIGN_MODELS":
			cfg.Models = append(cfg.Models, tagModels(vals, "design")...)
		}
		if strings.HasPrefix(strings.TrimSpace(line), "PROVIDER_OLLAMA_BASE_URL=") {
			baseURL = trimQuotes(strings.TrimSpace(line)[len("PROVIDER_OLLAMA_BASE_URL="):])
		}
	}
	cfg.OllamaBaseURL = baseURL

	if err := Save(cfg); err != nil {
		return false, err
	}

	// Migrate rotation position: copy last-selected model into rotation state.
	if err := migrateRotationState(stateDir, cfg); err != nil {
		return false, err
	}
	return true, nil
}

// tagModels wraps raw legacy model strings into Model entries with a tag.
func tagModels(raw []string, tag string) []Model {
	out := make([]Model, 0, len(raw))
	for _, id := range raw {
		m := Model{ID: id, Tags: []string{tag}}
		switch {
		case strings.HasPrefix(id, "native:"):
			m.Provider = strings.TrimPrefix(id, "native:")
			m.Location = LocationCloud
			m.Native = true
		case strings.HasSuffix(id, ":cloud"):
			m.Provider = "ollama"
			m.Location = LocationCloud
		default:
			m.Provider = "ollama"
			m.Location = LocationLocal
		}
		out = append(out, m)
	}
	return out
}

func trimQuotes(s string) string {
	return strings.Trim(s, `"`)
}
```

We referenced `Save` and `migrateRotationState` — add them. First `Save` in
`config.go`:

```go
// Save writes cfg to the config path using an atomic temp-file + rename.
func Save(cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		return err
	}
	var buf strings.Builder
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return err
	}
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, []byte(buf.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, Path())
}
```

Now the rotation-state migration. The legacy state file is two lines:
`<next_index>\n<last_selected>`. We'll read the last line and seed the new
per-tag rotation (full rotation logic comes in lesson 5). Add
`internal/rotation/state.go`:

```go
package rotation

import (
	"os"
	"path/filepath"
	"strings"
)

// LastSelected reads the last line of a legacy <mode>.state file.
func LastSelected(dir, mode string) string {
	data, err := os.ReadFile(filepath.Join(dir, "rotation-"+mode+".state"))
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}
```

Back in `migrate.go`, implement `migrateRotationState` — for now, just log the
last-selected models to stderr so nothing is lost. To avoid an import cycle
(`config` ← `rotation` ← `config` in lesson 5), read the legacy state file
directly in `migrate.go` rather than calling `rotation.LastSelected`:

```go
// lastSelected reads the last line of a legacy <mode>.state file.
// Defined here to avoid an import cycle with internal/rotation.
func lastSelected(stateDir, mode string) string {
	data, err := os.ReadFile(filepath.Join(stateDir, "rotation-"+mode+".state"))
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}

func migrateRotationState(stateDir string, cfg *Config) error {
	for _, mode := range []string{"code", "design"} {
		last := lastSelected(stateDir, mode)
		if last != "" {
			fmt.Fprintf(os.Stderr, "wt: migrated %s rotation; last selected: %s\n", mode, last)
		}
	}
	return nil
}
```

`migrate.go` already imports `os`, `path/filepath`, and `strings` for the rest of
the migration logic — no new imports are needed.

## Run It

With your real legacy `~/.config/agent-wt/models.conf`, run:

```bash
go run ./cmd/wt models
```

The first run should print a "migrated" notice to stderr, and `config.toml`
should now exist next to `models.conf`. A second run should not migrate again
(`config.toml` already exists).

## Try It Yourself

Write a unit test for `parseBashArray` that parses this line and checks the
name and values:

```bash
CODE_MODELS=("native:copilot" "deepseek-v4-pro:cloud")
```

<details>
<summary>Solution</summary>

```go
func TestParseBashArray(t *testing.T) {
	name, vals := parseBashArray(`CODE_MODELS=("native:copilot" "deepseek-v4-pro:cloud")`)
	if name != "CODE_MODELS" {
		t.Fatalf("name = %q, want CODE_MODELS", name)
	}
	if len(vals) != 2 || vals[0] != "native:copilot" || vals[1] != "deepseek-v4-pro:cloud" {
		t.Fatalf("vals = %v, want the two models", vals)
	}
}
```
</details>

## Checkpoint

```bash
git add -A && git commit -m "lesson 03: migration from legacy config" && git tag lesson-03
```
