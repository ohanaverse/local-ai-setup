package lifecycle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// The wired-limit preflight. A Sushi pack loads only when macOS lets the GPU
// wire enough memory (sysctl iogpu.wired_limit_mb); below that the load fails
// minutes in. The check runs before the others question and before any stop,
// so a start that cannot succeed fails at once with the fix and leaves every
// running model alone. wt only reads the sysctl: raising it needs sudo, and
// the user runs the command the error names.

// wiredLimitFamilies are the families whose start is checked against the
// wired limit. The gate is by family id: the provider table has no field for
// it.
var wiredLimitFamilies = map[string]bool{"sushi": true}

// modelDirFor resolves a family's model directory; a test swaps it to point a
// synthetic family at a temp pack directory.
var modelDirFor = localmodels.ModelDir

// readWiredLimit reads the wired limit in MiB (wiredlimit_darwin.go,
// wiredlimit_other.go). defaultEnv hands it to the engine as env.wiredLimit;
// IsolateProcessesForTest closes it, so no test reads the real sysctl.
var readWiredLimit = realReadWiredLimit

// preflighter is a backend with a check that must pass before a start touches
// anything: before the others question, any approved stop and the occupant's
// stop. It is optional; a backend without one has nothing to check.
type preflighter interface {
	preflight(ctx context.Context, e *env, cfg *config.Config, t Target) error
}

// WiredLimitError means the GPU wired-memory limit is below what the pack
// needs resident.
type WiredLimitError struct {
	Pack            string
	LimitMB, NeedMB int64
}

func (e *WiredLimitError) Error() string {
	return fmt.Sprintf("iogpu.wired_limit_mb is %d MiB but %s needs about %d MiB resident; raise it with: %s", e.LimitMB, e.Pack, e.NeedMB, wiredLimitHint())
}

// wiredLimitHint is the fix, for a caller that shows it apart from the error.
func wiredLimitHint() string {
	return "sudo sysctl iogpu.wired_limit_mb=59000 (resets on reboot)"
}

// memoryRefusalMarkers are the lower-case phrases that make a failed sushi
// warmup look like macOS refusing the memory. A pinned heuristic: Sushi's real
// wording is unmeasured, and llmbench's _MEMORY_REFUSAL holds the same list.
var memoryRefusalMarkers = []string{
	"out of memory", "insufficient memory", "not enough memory",
	"cannot allocate", "failed to allocate", "memory limit",
	"wired limit", "wired_limit", "iogpu",
}

// looksLikeMemoryRefusal reports whether msg contains a memory-refusal phrase,
// in any case.
func looksLikeMemoryRefusal(msg string) bool {
	msg = strings.ToLower(msg)
	for _, m := range memoryRefusalMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// packResidentBytes sums the sizes of the *.safetensors regular files directly
// in packDir: the weights held resident. Anything else (ngram_table.bin,
// config) is not counted. ok is false when the dir or a file cannot be read,
// or when it holds no safetensors.
func packResidentBytes(packDir string) (bytes int64, ok bool) {
	entries, err := os.ReadDir(packDir)
	if err != nil {
		return 0, false
	}
	found := false
	for _, de := range entries {
		if !strings.HasSuffix(de.Name(), ".safetensors") {
			continue
		}
		info, err := os.Stat(filepath.Join(packDir, de.Name()))
		if err != nil {
			return 0, false
		}
		if !info.Mode().IsRegular() {
			continue
		}
		bytes += info.Size()
		found = true
	}
	return bytes, found
}

// wiredNeedMiB is the wired memory a pack of packBytes needs: 110% of its
// safetensors, in MiB, floored. Integer only, the same rule as llmbench's.
func wiredNeedMiB(packBytes int64) int64 {
	return packBytes * 11 / 10 / (1 << 20)
}

// parseWiredLimit reads the output of `sysctl -n iogpu.wired_limit_mb
// hw.memsize`: the limit in MiB, then the RAM in bytes. A limit of 0 is
// macOS's default, 75% of RAM. Output without both numbers is an error, so
// the caller skips the check rather than judging on half an answer.
func parseWiredLimit(out string) (mb int64, ok bool, err error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		return 0, false, fmt.Errorf("want iogpu.wired_limit_mb and hw.memsize, got %q", out)
	}
	limit, err := strconv.ParseInt(strings.TrimSpace(lines[0]), 10, 64)
	if err != nil || limit < 0 {
		return 0, false, fmt.Errorf("iogpu.wired_limit_mb is not a size: %q", lines[0])
	}
	memsize, err := strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64)
	if err != nil || memsize <= 0 {
		return 0, false, fmt.Errorf("hw.memsize is not a size: %q", lines[1])
	}
	if limit == 0 {
		return (memsize * 3 / 4) >> 20, true, nil
	}
	return limit, true, nil
}

// preflight refuses a start of a wired-limit family whose pack needs more
// wired memory than the limit allows. What it cannot measure it does not
// judge: it prints one skip line and lets the start go on.
func (b processServer) preflight(ctx context.Context, e *env, cfg *config.Config, t Target) error {
	if !wiredLimitFamilies[b.family] {
		return nil
	}
	spec, err := b.spec()
	if err != nil {
		return err
	}
	if _, lerr := e.lookPath(spec.Binary); lerr != nil {
		return &BinaryMissingError{Binary: spec.Binary}
	}
	skip := func(why string, args ...any) error {
		routePrintf(ctx, "wt: skipping the memory check ("+why+")\n", args...)
		return nil
	}
	dir, err := modelDirFor(cfg, b.family)
	if err != nil {
		return skip("cannot find the model directory: %v", err)
	}
	bytes, ok := packResidentBytes(filepath.Join(dir, t.ModelName))
	if !ok {
		return skip("cannot read the safetensors of %s", t.ModelName)
	}
	limit, ok, err := e.wiredLimit()
	if err != nil {
		return skip("cannot read iogpu.wired_limit_mb: %v", err)
	}
	if !ok {
		return skip("iogpu.wired_limit_mb is not available")
	}
	if need := wiredNeedMiB(bytes); limit < need {
		return &WiredLimitError{Pack: t.ModelName, LimitMB: limit, NeedMB: need}
	}
	return nil
}
