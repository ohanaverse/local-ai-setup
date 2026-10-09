package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// The ollama provider row of cloudSyncRegistry names this address, and every
// ollama command must be pinned to it.
const testOllamaOrigin = "http://127.0.0.1:11434"

// fakeOllama stands in for the ollama CLI. It records every command with the
// origin it was pinned to, and changes nothing anywhere.
type fakeOllama struct {
	mu    sync.Mutex
	tags  []string
	fail  map[string]string // "list", "pull <tag>" or "rm <tag>" -> stderr
	calls []string
}

func stubOllama(t *testing.T, tags ...string) *fakeOllama {
	t.Helper()
	fake := &fakeOllama{tags: tags, fail: map[string]string{}}
	old := ollamaCLI
	ollamaCLI = func(_ context.Context, origin string, args ...string) (string, string, error) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		command := strings.Join(args, " ")
		fake.calls = append(fake.calls, origin+" "+command)
		if stderr, bad := fake.fail[command]; bad {
			return "", stderr, errors.New("exit status 1")
		}
		if args[0] != "list" {
			return "", "", nil
		}
		out := "NAME                    ID              SIZE      MODIFIED\n"
		for _, tag := range fake.tags {
			out += tag + "    0123456789ab    -         2 days ago\n"
		}
		return out, "", nil
	}
	t.Cleanup(func() { ollamaCLI = old })
	return fake
}

// changes are the recorded commands other than `list`, without the origin.
func (f *fakeOllama) changes() []string {
	var out []string
	for _, call := range f.calls {
		if command := strings.TrimPrefix(call, testOllamaOrigin+" "); command != "list" {
			out = append(out, command)
		}
	}
	return out
}

// TestOllamaSeamFailsClosed pins TestMain: with nothing stubbed, no test in
// this package runs the developer's ollama. An unstubbed catalog test fails
// with "not stubbed" instead of pulling a model onto, or removing one from,
// the machine it runs on.
func TestOllamaSeamFailsClosed(t *testing.T) {
	if _, _, err := ollamaCLI(context.Background(), testOllamaOrigin, "pull", "x:cloud"); err == nil || !strings.Contains(err.Error(), "not stubbed") {
		t.Errorf("ollamaCLI err = %v, want a not-stubbed failure", err)
	}
}

// TestOllamaRemoveReadsNotFoundAsDone pins the one failure of `ollama rm`
// that is not one: the tag is already gone. Anything else is an error that
// carries what ollama said, and `ollama pull` has no such exception.
func TestOllamaRemoveReadsNotFoundAsDone(t *testing.T) {
	ollama := stubOllama(t)
	ollama.fail["rm gone:cloud"] = "Error: model 'gone:cloud' not found"
	ollama.fail["rm locked:cloud"] = "Error: permission denied"
	ollama.fail["pull missing:cloud"] = "Error: pull model manifest: file does not exist"
	ctx := context.Background()
	if err := ollamaRemove(ctx, testOllamaOrigin, "gone:cloud"); err != nil {
		t.Errorf("rm of a tag that is not there: err = %v, want none", err)
	}
	if err := ollamaRemove(ctx, testOllamaOrigin, "locked:cloud"); err == nil || err.Error() != "`ollama rm locked:cloud` failed: Error: permission denied" {
		t.Errorf("rm that failed: err = %v", err)
	}
	if err := ollamaPull(ctx, testOllamaOrigin, "missing:cloud"); err == nil || err.Error() != "`ollama pull missing:cloud` failed: Error: pull model manifest: file does not exist" {
		t.Errorf("pull that failed: err = %v", err)
	}
	if err := ollamaPull(ctx, testOllamaOrigin, "fine:cloud"); err != nil {
		t.Errorf("pull that worked: err = %v", err)
	}
	want := []string{"rm gone:cloud", "rm locked:cloud", "pull missing:cloud", "pull fine:cloud"}
	if got := ollama.changes(); !reflect.DeepEqual(got, want) {
		t.Errorf("ollama commands = %q, want %q", got, want)
	}
}

// TestOllamaTagsReadsTheNameColumn pins the `ollama list` parser on real
// output shapes: the header and blank lines are skipped, and a size or date
// with spaces in it does not bleed into the name.
func TestOllamaTagsReadsTheNameColumn(t *testing.T) {
	old := ollamaCLI
	t.Cleanup(func() { ollamaCLI = old })
	ollamaCLI = func(context.Context, string, ...string) (string, string, error) {
		return "NAME                 ID      SIZE  MODIFIED\nglm-5.3:cloud  abc  -  2 days ago\nornith-1.5:35b  def  20 GB  1 week ago\n\n", "", nil
	}
	tags, err := ollamaTags(context.Background(), testOllamaOrigin)
	if want := []string{"glm-5.3:cloud", "ornith-1.5:35b"}; err != nil || !reflect.DeepEqual(tags, want) {
		t.Errorf("ollamaTags = (%q, %v), want %q", tags, err, want)
	}
	ollamaCLI = func(context.Context, string, ...string) (string, string, error) {
		return "", "", errors.New("the ollama command is not installed (not on PATH)")
	}
	if _, err := ollamaTags(context.Background(), testOllamaOrigin); err == nil || err.Error() != "the ollama command is not installed (not on PATH)" {
		t.Errorf("ollamaTags with no ollama: err = %v", err)
	}
}

// TestRealOllamaCLIPinsTheDaemon runs the real exec path against a stand-in
// `ollama` script and pins the one thing that matters about it: the command
// talks to the daemon the registry names, even when the shell exports
// another OLLAMA_HOST. Otherwise a pull or an rm could land on a daemon wt
// does not manage. It also pins that stdout and stderr come back apart, and
// that a missing ollama is said plainly.
func TestRealOllamaCLIPinsTheDaemon(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\necho \"host=$OLLAMA_HOST args=$*\"\necho \"to stderr\" >&2\n[ \"$1\" = rm ] && exit 3\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "ollama"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("OLLAMA_HOST", "http://elsewhere.invalid:1")

	stdout, stderr, err := realOllamaCLI(context.Background(), "http://127.0.0.1:11999", "pull", "x:cloud")
	if err != nil || stdout != "host=http://127.0.0.1:11999 args=pull x:cloud\n" || stderr != "to stderr\n" {
		t.Errorf("realOllamaCLI = (%q, %q, %v)", stdout, stderr, err)
	}
	if _, _, err := realOllamaCLI(context.Background(), "http://127.0.0.1:11999", "rm", "x:cloud"); err == nil {
		t.Error("a non-zero exit was not an error")
	}
	t.Setenv("PATH", t.TempDir())
	if _, _, err := realOllamaCLI(context.Background(), "http://127.0.0.1:11999", "list"); err == nil || err.Error() != "the ollama command is not installed (not on PATH)" {
		t.Errorf("with no ollama on PATH: err = %v", err)
	}
}

// TestOllamaFailureIsOneCleanLine pins how a failed ollama command is worded.
// `ollama pull` writes its progress, cursor escapes included, to stderr before
// its `Error:` line, and wt prints the failure after a `catalog: error:`
// prefix: handed the whole of stderr, every line after the first would lose
// the prefix a reader (or the cloud-sync skill) finds errors by, and the
// escapes would reach the terminal. One line comes back: the last one that
// says anything, which is where ollama puts its error.
func TestOllamaFailureIsOneCleanLine(t *testing.T) {
	ollama := stubOllama(t)
	progress := "pulling manifest \n\x1b[?25lpulling manifest \r\x1b[1G\x1b[K"
	ollama.fail["pull glm-5.3:cloud"] = progress + "Error: pull model manifest: file does not exist\n\n"
	ollama.fail["rm stuck:cloud"] = "Warning: something\nError: permission denied\n"
	ollama.fail["rm quiet:cloud"] = "\x1b[?25h\n \n"
	ollama.fail["list"] = "Warning: something\r\nError: could not connect to ollama server\r\n"
	ctx := context.Background()
	if err := ollamaPull(ctx, testOllamaOrigin, "glm-5.3:cloud"); err == nil || err.Error() != "`ollama pull glm-5.3:cloud` failed: Error: pull model manifest: file does not exist" {
		t.Errorf("pull: err = %q", err)
	}
	if err := ollamaRemove(ctx, testOllamaOrigin, "stuck:cloud"); err == nil || err.Error() != "`ollama rm stuck:cloud` failed: Error: permission denied" {
		t.Errorf("rm: err = %q", err)
	}
	// Nothing readable on stderr: why the command failed stands in.
	if err := ollamaRemove(ctx, testOllamaOrigin, "quiet:cloud"); err == nil || err.Error() != "`ollama rm quiet:cloud` failed: exit status 1" {
		t.Errorf("rm with only escapes on stderr: err = %q", err)
	}
	if _, err := ollamaTags(ctx, testOllamaOrigin); err == nil || err.Error() != "Error: could not connect to ollama server" {
		t.Errorf("list: err = %q", err)
	}
}

// TestOllamaRemoveNotFoundMustNameTheTag pins how narrowly "already gone" is
// read. Only ollama's own answer for a missing model, which names the tag,
// counts; any other failure that happens to contain "not found" (an HTTP 404
// page from something that is not ollama on that address) is an error.
// Otherwise wt would print "removed <tag>" and exit 0 for a tag that is
// still pulled.
func TestOllamaRemoveNotFoundMustNameTheTag(t *testing.T) {
	ollama := stubOllama(t)
	ollama.fail["rm gone:cloud"] = "pulling\nError: model 'gone:cloud' not found\n"
	ollama.fail["rm Mixed:cloud"] = "Error: Model 'mixed:cloud' Not Found"
	ollama.fail["rm kept:cloud"] = "Error: 404 page not found"
	ollama.fail["rm other:cloud"] = "Error: model 'different:cloud' not found"
	ctx := context.Background()
	for _, tag := range []string{"gone:cloud", "Mixed:cloud"} {
		if err := ollamaRemove(ctx, testOllamaOrigin, tag); err != nil {
			t.Errorf("rm %s, which ollama says is not there: err = %v, want none", tag, err)
		}
	}
	if err := ollamaRemove(ctx, testOllamaOrigin, "kept:cloud"); err == nil || err.Error() != "`ollama rm kept:cloud` failed: Error: 404 page not found" {
		t.Errorf("rm answered by a 404 page: err = %v, want a failure", err)
	}
	if err := ollamaRemove(ctx, testOllamaOrigin, "other:cloud"); err == nil {
		t.Error("rm answered with another model's not-found was read as done")
	}
}

// TestOllamaTimeoutIsSaidAsOne pins the wording when wt's own limit stops an
// ollama command. The killed process reports "signal: killed" (or whatever
// progress it had printed), which reads as a crash; the user has to be told
// it was a timeout, and how long wt waited, to know that trying again is
// reasonable. A tag whose rm timed out is never read as removed, whatever
// was on stderr by then.
func TestOllamaTimeoutIsSaidAsOne(t *testing.T) {
	old := ollamaCLI
	t.Cleanup(func() { ollamaCLI = old })
	ollamaCLI = func(ctx context.Context, _ string, args ...string) (string, string, error) {
		<-ctx.Done()
		return "", "pulling manifest \nError: model '" + args[len(args)-1] + "' not found\n", errors.New("signal: killed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := ollamaPull(ctx, testOllamaOrigin, "slow:cloud"); err == nil || err.Error() != "`ollama pull slow:cloud` timed out after 10m (wt's own limit)" {
		t.Errorf("pull: err = %q", err)
	}
	if err := ollamaRemove(ctx, testOllamaOrigin, "slow:cloud"); err == nil || err.Error() != "`ollama rm slow:cloud` timed out after 1m (wt's own limit)" {
		t.Errorf("rm: err = %q", err)
	}
	if _, err := ollamaTags(ctx, testOllamaOrigin); err == nil || err.Error() != "timed out after 30s (wt's own limit)" {
		t.Errorf("list: err = %q", err)
	}
}

// TestRealOllamaCLITimeoutIsNotHeldByAChild pins that wt's time limit holds
// when `ollama` on PATH is a wrapper script: the limit kills the script, but
// the command it started lives on holding the output pipes, and waiting for
// those to close would keep `wt cloud-sync` hung for as long as that child
// lives, with the registry already written and the routes not yet synced.
// The stand-in here is a shell script whose child sleeps; nothing real runs.
func TestRealOllamaCLITimeoutIsNotHeldByAChild(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ollama"), []byte("#!/bin/sh\n/bin/sleep 4\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	oldDelay := ollamaWaitDelay
	ollamaWaitDelay = 300 * time.Millisecond
	t.Cleanup(func() { ollamaWaitDelay = oldDelay })

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := realOllamaCLI(ctx, "http://127.0.0.1:11999", "pull", "x:cloud")
	if took := time.Since(start); err == nil || took > 2*time.Second {
		t.Errorf("realOllamaCLI returned after %s with err = %v; want an error within the limit plus the wait delay", took.Round(10*time.Millisecond), err)
	}
}
