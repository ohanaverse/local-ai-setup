# Lesson 5: Tag-based rotation

## Concept Intro

Rotation is the existing tool's way to cycle through models: each launch
advances to the next model in a group, so you get variety across coding
sessions. Today it's hard-wired to two groups (`code`, `design`) with two
state files. We generalize it to **tag-based rotation**: any tag can be a
rotation group, and the `--code`/`--design` flags just select which tag group
to rotate within (default from `config.DefaultTag`).

The legacy behavior also has a subtlety we keep: **cross-rotation skip**. When
you rotate through `code`, it avoids the model that `design` most recently
used (and vice-versa), so the two groups don't both land on the same model.

The rotation state is a small struct persisted to a file per tag group:
`rotation-<tag>.state`. It records the current index and the last selected
model. `Next()` picks the next usable model, writes the state, and returns it.

## New Syntax & Vocabulary

| Term | Meaning |
|---|---|
| `type Rotation struct` | Holds models + index + state file path. |
| `Next() (config.Model, bool)` | Advances rotation and returns the chosen model; `false` if none usable. |
| `os.MkdirAll` | Creates parent dirs as needed before writing state. |
| `os.WriteFile(..., 0o600)` | Writes state with restrictive perms (contains no secrets, but be tidy). |
| `strconv.Itoa` / `strconv.Atoi` | Convert int ↔ string for the index in state. |
| `sync.Mutex` | Guards the state file read/write (rotation may be triggered from a goroutine later). |
| `tag group` | The set of models whose tags include a given tag. |

## Worked Walkthrough

Create `internal/rotation/rotation.go`:

```go
package rotation

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// StateFile returns the state path for a tag group under the config dir.
func StateFile(cfgDir, tag string) string {
	return filepath.Join(cfgDir, "rotation-"+tag+".state")
}

// Rotation cycles through a tag group's models, advancing each Next() call.
type Rotation struct {
	mu       sync.Mutex
	tag      string
	models   []config.Model
	stateDir string
}

// New builds a Rotation for a tag group from the given models.
func New(tag string, models []config.Model, stateDir string) *Rotation {
	return &Rotation{tag: tag, models: models, stateDir: stateDir}
}

// Next returns the next model in rotation. otherTag is the tag group to
// cross-skip against (pass "" to disable). Returns false if the group is empty
// or no model is usable.
func (r *Rotation) Next(otherTag string) (config.Model, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.models) == 0 {
		return config.Model{}, false
	}

	stateDir := r.stateDir
	if stateDir == "" {
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, ".config")
		}
		stateDir = filepath.Join(base, "agent-wt")
	}

	// Load current index + last selected from disk.
	idx, last := r.loadState(stateDir)
	if idx < 0 || idx >= len(r.models) {
		idx = 0
	}

	// What did the other group last use? Avoid it.
	otherLast := ""
	if otherTag != "" {
		otherLast = LastSelected(stateDir, otherTag)
	}

	n := len(r.models)
	selected := r.models[idx]
	// Basic usable filter: skip models the other group just used.
	for i := 0; i < n; i++ {
		cand := r.models[(idx+i)%n]
		if otherLast != "" && cand.ID == otherLast {
			continue
		}
		selected = cand
		idx = (idx + i + 1) % n
		break
	}

	// Persist next index and last-selected.
	next := idx
	r.saveState(stateDir, next, selected.ID)

	return selected, true
}

// loadState reads "<index>\n<last>\n" from the state file.
func (r *Rotation) loadState(dir string) (int, string) {
	data, err := os.ReadFile(StateFile(dir, r.tag))
	if err != nil {
		return 0, ""
	}
	parts := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(parts) == 0 {
		return 0, ""
	}
	idx, _ := strconv.Atoi(parts[0])
	last := ""
	if len(parts) > 1 {
		last = parts[1]
	}
	return idx, last
}

// saveState writes "<index>\n<last>\n" atomically via temp file + rename.
func (r *Rotation) saveState(dir string, idx int, last string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf("%d\n%s\n", idx, last)
	tmp := StateFile(dir, r.tag) + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, StateFile(dir, r.tag))
}
```

Note we reuse `LastSelected` from lesson 3's `state.go` to read the other
group's last model.

### Wiring into the command

Add a helper that builds a rotation from the config:

```go
// in internal/rotation/rotation.go
// ForTag returns a Rotation over cfg's models tagged with tag.
func ForTag(cfg *config.Config, tag string) *Rotation {
	return New(tag, cfg.ModelsWithTag(tag), "")
}
```

And expose it through a small CLI flag so you can test rotation non-interactively
before the TUI (lesson 12). Add to `rootCmd` a hidden `--rotate-tag` flag:

```go
// test-only for this lesson
cmd.Flags().String("rotate-tag", "", "print next model in the given tag group")
```

In the root `RunE`, before the TUI placeholder:

```go
if tag, _ := cmd.Flags().GetString("rotate-tag"); tag != "" {
	cfg, _ := config.Load()
	r := rotation.ForTag(cfg, tag)
	m, ok := r.Next("")
	if !ok {
		return fmt.Errorf("no models tagged %q", tag)
	}
	fmt.Println(m.ID)
	return nil
}
```

## Run It

```bash
go run ./cmd/wt --rotate-tag code
go run ./cmd/wt --rotate-tag code   # advances each time
go run ./cmd/wt --rotate-tag design
```

Each `code` invocation prints the next model in the code group and advances the
state; `design` behaves independently. Check the state file:

```bash
cat "$XDG_CONFIG_HOME/agent-wt/rotation-code.state"
```

## Try It Yourself

Add the cross-rotation behavior: call `Next("design")` for the code rotation
and verify it skips the model `design` last used when that model is also in
the code group.

<details>
<summary>Solution</summary>

```go
cfg, _ := config.Load()
code := rotation.ForTag(cfg, "code")
design := rotation.ForTag(cfg, "design")
// Advance design once to set its last.
design.Next("")
// Now rotate code, avoiding design's last pick.
m, _ := code.Next("design")
if m.ID == designLast {
	t.Errorf("code rotation selected %q which design just used", m.ID)
}
```
</details>

## Checkpoint

```bash
git add -A && git commit -m "lesson 05: tag-based rotation" && git tag lesson-05
```
