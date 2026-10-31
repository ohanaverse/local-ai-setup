package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
)

// realPackBytes is the summed *.safetensors size of the real Sushi pack, the
// figure both languages must turn into the same 51,934 MiB.
const realPackBytes int64 = 49_506_491_792

// sparseFile creates path at size bytes without writing them (os.Truncate).
func sparseFile(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, size); err != nil {
		t.Fatal(err)
	}
}

// preflightFamily makes the synthetic family sushix a wired-limit family
// whose model directory is a temp dir, with a pack named pack holding
// packBytes of safetensors (none when packBytes is 0). It returns an env whose
// lookPath finds the binary and whose wiredLimit reports limit, and the cfg.
func preflightFamily(t *testing.T, pack string, packBytes, limit int64) (*env, *config.Config) {
	t.Helper()
	spec := provider.ProcessSpec{
		Binary: "sushix", Subcommand: "serve", PortFlag: "--port", Spawn: sushiSpawn,
		Ready: provider.Ready{Kind: provider.ReadyHTTP, Path: "/health"}, WarmupSeconds: 2,
	}
	port := closedPort(t)
	stubFamily(t, "sushix", spec, provider.FamilySpec{Name: "sushix", Tenancy: provider.Exclusive, Scope: provider.ScopePort, Process: &spec}, port)
	wiredLimitFamilies["sushix"] = true
	t.Cleanup(func() { delete(wiredLimitFamilies, "sushix") })
	models := t.TempDir()
	oldDir := modelDirFor
	modelDirFor = func(_ *config.Config, family string) (string, error) {
		if family != "sushix" {
			t.Fatalf("modelDirFor(%q), want sushix", family)
		}
		return models, nil
	}
	t.Cleanup(func() { modelDirFor = oldDir })
	if packBytes > 0 {
		sparseFile(t, filepath.Join(models, pack, "model-00001.safetensors"), packBytes)
	}
	e := testEnv()
	e.lookPath = func(string) (string, error) { return os.Args[0], nil }
	e.wiredLimit = func() (int64, bool, error) { return limit, true, nil }
	return e, provCfg("sushix", "http://127.0.0.1:1/v1")
}

// TestPackResidentBytesCountsOnlySafetensors pins what the memory check sizes:
// the *.safetensors files directly in the pack dir, nothing else. The
// ngram_table.bin beside them is not held resident, and counting its 32 GiB
// would refuse every start; a missing dir is "cannot tell", not zero.
func TestPackResidentBytesCountsOnlySafetensors(t *testing.T) {
	dir := t.TempDir()
	sparseFile(t, filepath.Join(dir, "model-00001-of-00002.safetensors"), 30000<<20)
	sparseFile(t, filepath.Join(dir, "model-00002-of-00002.safetensors"), 17213<<20)
	sparseFile(t, filepath.Join(dir, "ngram_table.bin"), 32<<30)
	sparseFile(t, filepath.Join(dir, "config.json"), 2048)
	if got, ok := packResidentBytes(dir); !ok || got != 47213<<20 {
		t.Errorf("packResidentBytes = %d, %v; want %d, true", got, ok, int64(47213<<20))
	}

	real := t.TempDir()
	sparseFile(t, filepath.Join(real, "a.safetensors"), realPackBytes-1000)
	sparseFile(t, filepath.Join(real, "b.safetensors"), 1000)
	if got, ok := packResidentBytes(real); !ok || got != realPackBytes {
		t.Errorf("real pack: packResidentBytes = %d, %v; want %d, true", got, ok, realPackBytes)
	}

	if _, ok := packResidentBytes(filepath.Join(dir, "missing")); ok {
		t.Error("missing dir: ok = true, want false")
	}
	empty := t.TempDir()
	sparseFile(t, filepath.Join(empty, "ngram_table.bin"), 10)
	if _, ok := packResidentBytes(empty); ok {
		t.Error("dir with no safetensors: ok = true, want false")
	}
}

// TestWiredNeedIs110PercentFloored pins the one integer rule both languages
// share: floor(bytes * 11 / 10 / MiB), no float and no rounding up, so the
// Go and Python checks never disagree about the same pack (51,934 MiB).
func TestWiredNeedIs110PercentFloored(t *testing.T) {
	for bytes, want := range map[int64]int64{47213 << 20: 51934, realPackBytes: 51934, 1 << 20: 1, 0: 0} {
		if got := wiredNeedMiB(bytes); got != want {
			t.Errorf("wiredNeedMiB(%d) = %d, want %d", bytes, got, want)
		}
	}
}

// TestParseWiredLimit pins the reading of `sysctl -n iogpu.wired_limit_mb
// hw.memsize`: a set limit reads as itself, 0 (macOS's default) as 75% of RAM
// in MiB, and anything without both lines or not numeric as unreadable, so
// the check is skipped rather than judged on a guess.
func TestParseWiredLimit(t *testing.T) {
	for out, want := range map[string]int64{"59000\n68719476736\n": 59000, "0\n68719476736\n": 49152} {
		if got, ok, err := parseWiredLimit(out); err != nil || !ok || got != want {
			t.Errorf("parseWiredLimit(%q) = %d, %v, %v; want %d, true, nil", out, got, ok, err, want)
		}
	}
	for _, out := range []string{"0\n", "59000\n", "", "x\ny\n", "59000\nmany\n", "-1\n68719476736\n"} {
		if _, _, err := parseWiredLimit(out); err == nil {
			t.Errorf("parseWiredLimit(%q): err = nil, want an error", out)
		}
	}
}

// TestWiredLimitErrorNamesTheFix pins the refusal's text: the user must read
// both numbers and the exact command that fixes it, and that it does not
// survive a reboot, or the next start after a restart fails the same way.
func TestWiredLimitErrorNamesTheFix(t *testing.T) {
	msg := (&WiredLimitError{Pack: "Org/Pack", LimitMB: 49152, NeedMB: 51934}).Error()
	for _, want := range []string{"sudo sysctl iogpu.wired_limit_mb=59000", "resets on reboot", "49152", "51934", "Org/Pack"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
	if hint := wiredLimitHint(); hint != "sudo sysctl iogpu.wired_limit_mb=59000 (resets on reboot)" {
		t.Errorf("wiredLimitHint() = %q", hint)
	}
}

// TestSushiPreflightFailsBelowNeedAndPassesAbove pins the check's verdicts: a
// limit below the pack's need refuses with both numbers, one at or above it
// passes, and every case it cannot judge (pack unreadable, limit unreadable,
// model dir unresolved) passes with a one-line skip notice instead of
// blocking a start that may well succeed.
func TestSushiPreflightFailsBelowNeedAndPassesAbove(t *testing.T) {
	target := Target{ProviderID: "sushix", ModelName: "Org/Pack"}
	for _, tc := range []struct {
		limit   int64
		refused bool
	}{{49152, true}, {51933, true}, {51934, false}, {59000, false}} {
		e, cfg := preflightFamily(t, "Org/Pack", 47213<<20, tc.limit)
		err := processServer{"sushix"}.preflight(context.Background(), e, cfg, target)
		var wl *WiredLimitError
		if tc.refused {
			if !errors.As(err, &wl) || *wl != (WiredLimitError{Pack: "Org/Pack", LimitMB: tc.limit, NeedMB: 51934}) {
				t.Errorf("limit %d: err = %#v, want *WiredLimitError{%d, 51934}", tc.limit, err, tc.limit)
			}
		} else if err != nil {
			t.Errorf("limit %d: err = %v, want nil", tc.limit, err)
		}
	}

	skips := map[string]func(*env){
		"pack unreadable": func(e *env) {},
		"limit unreadable": func(e *env) {
			e.wiredLimit = func() (int64, bool, error) { return 0, false, errors.New("sysctl: unknown oid") }
		},
		"limit unavailable": func(e *env) {
			e.wiredLimit = func() (int64, bool, error) { return 0, false, nil }
		},
		"model dir unresolved": func(e *env) {
			modelDirFor = func(*config.Config, string) (string, error) { return "", errors.New("no HOME") }
		},
	}
	for name, setup := range skips {
		packBytes := int64(47213 << 20)
		if name == "pack unreadable" {
			packBytes = 0
		}
		e, cfg := preflightFamily(t, "Org/Pack", packBytes, 1)
		setup(e)
		var out strings.Builder
		err := processServer{"sushix"}.preflight(withRouteOutput(context.Background(), &out), e, cfg, target)
		if err != nil {
			t.Errorf("%s: err = %v, want nil (skipped)", name, err)
		}
		if got := out.String(); strings.Count(got, "\n") != 1 || !strings.Contains(got, "skipping the memory check (") {
			t.Errorf("%s: output = %q, want one 'skipping the memory check (...)' line", name, got)
		}
	}
}

// TestSushiPreflightMissingBinary pins that a wired-limit family whose binary
// is not on PATH fails with *BinaryMissingError, the same error the start
// would give, rather than a memory verdict about a server that cannot run.
func TestSushiPreflightMissingBinary(t *testing.T) {
	e, cfg := preflightFamily(t, "Org/Pack", 47213<<20, 59000)
	e.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	err := processServer{"sushix"}.preflight(context.Background(), e, cfg, Target{ProviderID: "sushix", ModelName: "Org/Pack"})
	var bm *BinaryMissingError
	if !errors.As(err, &bm) || bm.Binary != "sushix" {
		t.Errorf("err = %v, want *BinaryMissingError for sushix", err)
	}
}

// TestPreflightRunsBeforeAnyOtherProviderIsStopped pins where the check sits:
// before the others question and before any approved stop. A start that is
// going to fail on the wired limit must not first stop the user's other
// models, which would leave them with nothing running. The passing control
// proves the same setup does stop the other model once the check passes.
func TestPreflightRunsBeforeAnyOtherProviderIsStopped(t *testing.T) {
	for _, tc := range []struct {
		limit int64
		want  []string
	}{{49152, nil}, {59000, []string{"ollama:stopModel:x:1b"}}} {
		var calls []string
		e, cfg := preflightFamily(t, "Org/Pack", 47213<<20, tc.limit)
		snap := localmodels.Snapshot{Entries: []localmodels.Entry{running("ollama", "ollama/x", "x:1b")}}
		e.inventory = func(*config.Config) localmodels.Snapshot { return snap }
		e.backends = map[string]backend{
			"ollama": &fakeBackend{ten: Shared, calls: &calls, family: "ollama"},
			"sushix": processServer{"sushix"},
		}
		stopped := recordStopped(e)
		e.spawn = func(pidProcess, string, []string) (*spawned, error) { return nil, errors.New("spawn stubbed") }
		err := start(context.Background(), e, cfg, Target{ProviderID: "sushix", ModelName: "Org/Pack"},
			Options{Others: OthersAnswer{Decided: true, Stop: []string{"ollama/x"}}})
		var occ *OccupiedError
		if errors.As(err, &occ) {
			t.Errorf("limit %d: err = %v, want no OccupiedError", tc.limit, err)
		}
		var wl *WiredLimitError
		if refused := errors.As(err, &wl); refused != (tc.limit < 51934) {
			t.Errorf("limit %d: err = %v, want *WiredLimitError only below the need", tc.limit, err)
		}
		if !reflect.DeepEqual(calls, tc.want) {
			t.Errorf("limit %d: other backend calls = %v, want %v", tc.limit, calls, tc.want)
		}
		if tc.want == nil && len(*stopped) != 0 {
			t.Errorf("limit %d: route hook = %v, want nothing stopped", tc.limit, *stopped)
		}
		if tc.want == nil {
			// Unanswered, the start would ask about ollama/x; the refusal
			// comes first, so the user is never asked a pointless question.
			err := start(context.Background(), e, cfg, Target{ProviderID: "sushix", ModelName: "Org/Pack"}, Options{})
			if !errors.As(err, &wl) || errors.As(err, &occ) {
				t.Errorf("unanswered: err = %v, want *WiredLimitError, not an OccupiedError", err)
			}
		}
	}
}

// TestPreflightIsSkippedForOtherFamilies pins the gate: only a family in
// wiredLimitFamilies is checked, so an mtplx start never reads the sysctl or
// the model dir, and a machine without iogpu never sees a skip line for it.
func TestPreflightIsSkippedForOtherFamilies(t *testing.T) {
	e := testEnv()
	e.wiredLimit = func() (int64, bool, error) { t.Fatal("wiredLimit read for mtplx"); return 0, false, nil }
	e.lookPath = func(string) (string, error) { t.Fatal("lookPath called for mtplx"); return "", nil }
	oldDir := modelDirFor
	modelDirFor = func(*config.Config, string) (string, error) { t.Fatal("modelDirFor called for mtplx"); return "", nil }
	t.Cleanup(func() { modelDirFor = oldDir })
	var out strings.Builder
	err := processServer{"mtplx"}.preflight(withRouteOutput(context.Background(), &out), e, &config.Config{}, Target{ProviderID: "mtplx", ModelName: "Org/M"})
	if err != nil || out.Len() != 0 {
		t.Errorf("err = %v, output = %q; want nil and nothing printed", err, out.String())
	}
}

// TestIsolateProcessesForTestClosesTheSysctl pins that the test binary cannot
// read the real machine's wired limit: TestMain's IsolateProcessesForTest
// closes readWiredLimit, so a test that forgets to stub e.wiredLimit gets the
// skip path, never a verdict that depends on the developer's Mac.
func TestIsolateProcessesForTestClosesTheSysctl(t *testing.T) {
	if _, _, err := readWiredLimit(); err == nil {
		t.Fatal("readWiredLimit() succeeded; want the isolated seam to refuse")
	}
	if _, _, err := defaultEnv().wiredLimit(); err == nil {
		t.Fatal("defaultEnv().wiredLimit() succeeded; want the isolated seam to refuse")
	}
}

// TestLooksLikeMemoryRefusal verifies the heuristic that decides a warmup
// failure is macOS refusing the memory: every phrase of memoryRefusalMarkers
// matches (any case), and text that is not about memory does not. A false
// positive tells the user to raise a limit that was never the problem.
func TestLooksLikeMemoryRefusal(t *testing.T) {
	for _, m := range memoryRefusalMarkers {
		if !looksLikeMemoryRefusal("HTTP 500: " + strings.ToUpper(m) + " while loading") {
			t.Errorf("marker %q did not match", m)
		}
	}
	for msg, want := range map[string]bool{
		"HTTP 500: Out of memory allocating Metal buffer": true,
		"IOGPU command buffer":                            true,
		"No default model configured":                     false,
		"":                                                false,
	} {
		if got := looksLikeMemoryRefusal(msg); got != want {
			t.Errorf("looksLikeMemoryRefusal(%q) = %v, want %v", msg, got, want)
		}
	}
}

// sushiWarmup starts the synthetic sushix family with a chat that answers
// status and body, and returns the start error and the env.
func sushiWarmup(t *testing.T, status int, body string) (error, *env) {
	t.Helper()
	t.Setenv("LIFECYCLE_HELPER_CHAT_BODY", body)
	e, cfg, _, _, _ := sushiEnv(t, "Org/Pack", status, 0)
	err := processServer{"sushix"}.start(context.Background(), e, cfg, Target{ProviderID: "sushix", ModelName: "Org/Pack"}, func(Stage) {})
	return err, e
}

// TestSushiWarmupMemoryRefusalAppendsTheHint verifies a sushi warmup that
// fails with a memory-refusal body ends with the wired-limit fix, still names
// the HTTP failure, and leaves no server behind: the user sees the command to
// run instead of a bare 500.
func TestSushiWarmupMemoryRefusalAppendsTheHint(t *testing.T) {
	err, e := sushiWarmup(t, 500, `{"error":"out of memory"}`)
	if err == nil {
		t.Fatal("start succeeded, want a warmup failure")
	}
	for _, want := range []string{"sudo sysctl iogpu.wired_limit_mb=59000", "resets on reboot", "HTTP 500", "log tail"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, missing %q", err, want)
		}
	}
	if !strings.HasSuffix(err.Error(), wiredLimitHint()) {
		t.Errorf("err = %q, want it to end with the hint", err)
	}
	if pf, rec := filesLeftFor(e, "sushix"); pf || rec {
		t.Errorf("files left = %v, %v, want none", pf, rec)
	}
}

// TestSushiWarmupMemoryRefusalInTheLogTailGetsTheHint verifies the match runs
// on the log tail too: the refusal may be only in the server's log while the
// HTTP body is generic.
func TestSushiWarmupMemoryRefusalInTheLogTailGetsTheHint(t *testing.T) {
	t.Setenv("LIFECYCLE_HELPER_LOG_LINE", "wired limit too low")
	err, _ := sushiWarmup(t, 500, `{"error":"load failed"}`)
	if err == nil || !strings.Contains(err.Error(), "sudo sysctl iogpu.wired_limit_mb=59000") {
		t.Errorf("err = %v, want the hint", err)
	}
}

// TestSushiWarmup503GetsNoHint verifies sushi's ordinary 503 warmup failure
// is not mistaken for a memory refusal.
func TestSushiWarmup503GetsNoHint(t *testing.T) {
	err, _ := sushiWarmup(t, 503, "")
	if err == nil || strings.Contains(err.Error(), "sudo sysctl") {
		t.Errorf("err = %v, want a failure without the hint", err)
	}
}

// TestMtplxWarmupMemoryRefusalGetsNoHint verifies the hint is sushi's alone:
// an mtplx warmup failing with the same words reads as it always has. The
// spec is a copy of mtplx's at a 1 s budget to keep the test short.
func TestMtplxWarmupMemoryRefusalGetsNoHint(t *testing.T) {
	e, cfg, port, _ := mtplxEnv(t, "serve", 0)
	t.Setenv("LIFECYCLE_HELPER_CHAT_STATUS", "500")
	t.Setenv("LIFECYCLE_HELPER_CHAT_BODY", `{"error":"out of memory"}`)
	spec, _ := provider.ProcessFor("mtplx")
	spec.WarmupSeconds = 1
	facts, _ := provider.FamilyByName("mtplx")
	stubFamily(t, "mtplx", spec, facts, port)
	err := processServer{"mtplx"}.start(context.Background(), e, cfg, Target{ProviderID: "mtplx", ModelName: "Org/Model"}, func(Stage) {})
	if err == nil || !strings.Contains(err.Error(), "out of memory") {
		t.Fatalf("err = %v, want a warmup failure carrying the body (else the check proves nothing)", err)
	}
	if strings.Contains(err.Error(), "sudo sysctl") {
		t.Errorf("err = %v, want no hint on an mtplx warmup failure", err)
	}
}
