package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// makeTargetBody returns the recipe lines of one wt/Makefile target (every
// line after "name:" up to the next target or blank line), so a test can
// assert on the target alone.
func makeTargetBody(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	lines := strings.Split(string(data), "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, name+":") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("Makefile has no %q target", name)
	}
	var body []string
	for _, l := range lines[start+1:] {
		if l == "" || (!strings.HasPrefix(l, "\t") && !strings.HasPrefix(l, "ifeq") && !strings.HasPrefix(l, "endif") && !strings.HasPrefix(l, "#")) {
			break
		}
		body = append(body, l)
	}
	return strings.Join(body, "\n")
}

// TestMakeInstallListsBothBinaries pins that wt is the same program under two
// names: build, install, uninstall and clean must each name wt-session and wt,
// so neither name can be left stale or stranded by one target forgetting it.
func TestMakeInstallListsBothBinaries(t *testing.T) {
	for _, target := range []string{"build", "install", "uninstall", "clean"} {
		body := makeTargetBody(t, target)
		if !strings.Contains(body, "wt-session") {
			t.Errorf("make %s does not name wt-session:\n%s", target, body)
		}
		if !regexp.MustCompile(`(^|[^-\w])wt([^-\w]|$)`).MatchString(strings.ReplaceAll(body, "wt-session", "")) {
			t.Errorf("make %s does not name wt:\n%s", target, body)
		}
	}
}

// TestBothBinariesReportTheSameVersion builds the command package twice under
// the two names the Makefile installs and checks both print the same
// "wt <version>" line (style guide N1/S6), so a script calling either name
// reads one version.
func TestBothBinariesReportTheSameVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	dir := t.TempDir()
	want := ""
	for _, name := range []string{"wt-session", "wt"} {
		out := filepath.Join(dir, name)
		build := exec.Command("go", "build", "-ldflags", "-X main.version=9.9.9", "-o", out, ".")
		if b, err := build.CombinedOutput(); err != nil {
			t.Fatalf("go build %s: %v\n%s", name, err, b)
		}
		b, err := exec.Command(out, "--version").Output()
		if err != nil {
			t.Fatalf("%s --version: %v", name, err)
		}
		got := strings.TrimSpace(string(b))
		if !strings.HasPrefix(got, "wt ") {
			t.Errorf("%s --version = %q, want a %q line", name, got, "wt <version>")
		}
		if want == "" {
			want = got
		} else if got != want {
			t.Errorf("%s --version = %q, other binary printed %q", name, got, want)
		}
	}
	if want != "wt 9.9.9" {
		t.Errorf("version line = %q, want %q", want, "wt 9.9.9")
	}
}
