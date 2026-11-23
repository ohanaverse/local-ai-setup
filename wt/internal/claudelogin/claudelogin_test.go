package claudelogin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeClaude is a stand-in for the claude binary: `--version` prints the file
// FAKE_VERSION names, `auth status --json` prints the file FAKE_STATUS names
// and exits FAKE_EXIT (0 by default), FAKE_SLEEP makes it hang, and when
// FAKE_ENV_DUMP is set every call appends its environment there.
const fakeClaude = `#!/bin/sh
[ -n "$FAKE_ENV_DUMP" ] && env >> "$FAKE_ENV_DUMP"
[ -n "$FAKE_SLEEP" ] && exec sleep "$FAKE_SLEEP"
case "$1" in
--version) [ -n "$FAKE_VERSION" ] && cat "$FAKE_VERSION"; exit 0 ;;
auth) [ -n "$FAKE_STATUS" ] && cat "$FAKE_STATUS"; exit ${FAKE_EXIT:-0} ;;
esac
exit 2
`

// fake writes the fake binary and its two answers into a scratch directory
// and returns the binary's path and the base environment naming the answers.
func fake(t *testing.T, status, version string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(fakeClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	st, ver := filepath.Join(dir, "status.json"), filepath.Join(dir, "version.txt")
	if err := os.WriteFile(st, []byte(status), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ver, []byte(version), 0o600); err != nil {
		t.Fatal(err)
	}
	return bin, []string{"PATH=/usr/bin:/bin", "FAKE_STATUS=" + st, "FAKE_VERSION=" + ver}
}

const goodStatus = `{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","email":"someone@example.com","orgId":"org-SECRETORG","orgName":"Example Org Name","subscriptionType":"max","loginExpiresAt":"2030-01-02T03:04:05Z"}`

// TestCheckScrubsCredentialEnv proves the check runs claude with every
// credential variable removed (the keys, tokens, base url, the custom
// headers and every CLAUDE_CODE_USE_* switch), so `claude auth status`
// reports the user's stored login and not an env credential, which would read
// as a false positive.
func TestCheckScrubsCredentialEnv(t *testing.T) {
	bin, env := fake(t, goodStatus, "2.1.330 (Claude Code)\n")
	dump := filepath.Join(t.TempDir(), "env")
	env = append(env, "FAKE_ENV_DUMP="+dump,
		"ANTHROPIC_API_KEY=sk-FAKE", "ANTHROPIC_AUTH_TOKEN=FAKE", "ANTHROPIC_BASE_URL=http://127.0.0.1:9",
		"CLAUDE_CODE_OAUTH_TOKEN=FAKE", "CLAUDE_CODE_USE_BEDROCK=1", "CLAUDE_CODE_USE_VERTEX=1",
		"CLAUDE_CODE_USE_SOMETHINGNEW=1", "ANTHROPIC_CUSTOM_HEADERS=x-wt-model-token: FAKE", "KEEP_ME=1",
		"CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR=3", "CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR=4")
	st, err := Check(context.Background(), bin, env)
	if err != nil || !st.OK() {
		t.Fatalf("Check = %+v, %v; want a login", st, err)
	}
	b, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		name, _, _ := strings.Cut(line, "=")
		switch {
		case name == "ANTHROPIC_API_KEY", name == "ANTHROPIC_AUTH_TOKEN", name == "ANTHROPIC_BASE_URL",
			name == "CLAUDE_CODE_OAUTH_TOKEN", name == "ANTHROPIC_CUSTOM_HEADERS", strings.HasPrefix(name, "CLAUDE_CODE_USE_"),
			name == "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR", name == "CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR":
			t.Errorf("claude saw %s", name)
		}
	}
	if !strings.Contains(string(b), "KEEP_ME=1") {
		t.Error("an unrelated variable was dropped")
	}
}

// TestCheckAcceptsOnlyClaudeAILogin proves only a claude.ai login on the
// first-party provider counts: an env-token login (oauth_token), no login, a
// non-zero exit, garbage output, a missing loggedIn and a hang past the
// deadline all mean no login, so a launch never sends a login it did not see.
func TestCheckAcceptsOnlyClaudeAILogin(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		extra        []string
		ok, timeout  bool
	}{
		{name: "claude.ai", status: goodStatus, ok: true},
		{name: "oauth_token", status: `{"loggedIn":true,"authMethod":"oauth_token","apiProvider":"firstParty"}`},
		{name: "api key", status: `{"loggedIn":true,"authMethod":"api_key","apiProvider":"firstParty"}`},
		{name: "bedrock", status: `{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"bedrock"}`},
		{name: "none", status: `{"loggedIn":false,"authMethod":"none","apiProvider":"firstParty"}`},
		{name: "exit 1", status: goodStatus, extra: []string{"FAKE_EXIT=1"}},
		{name: "garbage", status: "not json at all"},
		{name: "no loggedIn", status: `{"authMethod":"claude.ai","apiProvider":"firstParty"}`},
		{name: "timeout", status: goodStatus, extra: []string{"FAKE_SLEEP=30"}, timeout: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, env := fake(t, tc.status, "2.1.330\n")
			ctx := context.Background()
			if tc.timeout {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 300*time.Millisecond)
				defer cancel()
			}
			start := time.Now()
			st, err := Check(ctx, bin, append(env, tc.extra...))
			if st.OK() != tc.ok {
				t.Errorf("OK = %v, want %v (status %+v, err %v)", st.OK(), tc.ok, st, err)
			}
			if tc.timeout && time.Since(start) > 5*time.Second {
				t.Errorf("the check took %v past its deadline", time.Since(start))
			}
		})
	}
}

// TestCheckNeverReturnsPersonalFields proves the email, organization and
// subscription fields of claude's output are never kept: neither the Status
// nor any error text (success, garbage, non-zero exit) holds them.
func TestCheckNeverReturnsPersonalFields(t *testing.T) {
	personal := []string{"someone@example.com", "SECRETORG", "Example Org Name", "max"}
	for _, extra := range [][]string{nil, {"FAKE_EXIT=1"}} {
		bin, env := fake(t, goodStatus, "2.1.330\n")
		st, err := Check(context.Background(), bin, append(env, extra...))
		text := strings.Join([]string{st.Method, st.Provider, st.Version}, " ")
		if err != nil {
			text += " " + err.Error()
		}
		for _, p := range personal {
			if strings.Contains(text, p) {
				t.Errorf("%q leaked into %q", p, text)
			}
		}
	}
	bin, env := fake(t, `garbage someone@example.com SECRETORG`, "2.1.330\n")
	_, err := Check(context.Background(), bin, env)
	if err == nil || strings.Contains(err.Error(), "someone@") || strings.Contains(err.Error(), "SECRETORG") {
		t.Errorf("garbage err = %v, want a fixed text", err)
	}
}

// TestCheckReadsVersionAndExpiry pins the two non-login fields: the version
// from `claude --version`, compared numerically with MinVersion (2.1.200 is
// older, 2.1.330 newer, garbage unknown), and loginExpiresAt as RFC 3339 or
// epoch milliseconds.
func TestCheckReadsVersionAndExpiry(t *testing.T) {
	bin, env := fake(t, goodStatus, "2.1.330 (Claude Code)\n")
	st, err := Check(context.Background(), bin, env)
	if err != nil || st.Version != "2.1.330" || !st.VersionAtLeast(MinVersion) {
		t.Errorf("version = %q (%v)", st.Version, err)
	}
	if !st.ExpiresAt.Equal(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("ExpiresAt = %v", st.ExpiresAt)
	}
	for v, want := range map[string]bool{"2.1.200": false, "2.1.227": true, "2.2.0": true, "10.0.0": true, "": false, "garbage": false} {
		if got := (Status{Version: v}).VersionAtLeast(MinVersion); got != want {
			t.Errorf("VersionAtLeast(%q) = %v, want %v", v, got, want)
		}
	}
	ms := `{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","loginExpiresAt":1893553445000}`
	bin, env = fake(t, ms, "2.1.330\n")
	st, _ = Check(context.Background(), bin, env)
	if st.ExpiresAt.Unix() != 1893553445 {
		t.Errorf("ExpiresAt from ms = %v", st.ExpiresAt)
	}
}

// TestCheckTimeoutKillsTheProcessGroupAndSaysWhy proves a claude (or a shim
// in front of it) that forks a child and hangs is killed with its whole
// process group when the check gives up, so no orphan outlives wt, and that
// the error says the real reason: the check's own deadline is ErrTimeout, a
// caller's cancel or deadline is ErrCancelled.
func TestCheckTimeoutKillsTheProcessGroupAndSaysWhy(t *testing.T) {
	const forking = "#!/bin/sh\nsleep 30 &\necho $! > \"$FAKE_CHILD_PID\"\nwait\n"
	run := func(t *testing.T, parent time.Duration, own time.Duration) (error, string) {
		t.Helper()
		dir := t.TempDir()
		bin := filepath.Join(dir, "claude")
		if err := os.WriteFile(bin, []byte(forking), 0o755); err != nil {
			t.Fatal(err)
		}
		pidFile := filepath.Join(dir, "child.pid")
		ctx, cancel := context.WithTimeout(context.Background(), parent)
		defer cancel()
		_, err := check(ctx, bin, []string{"PATH=/usr/bin:/bin", "FAKE_CHILD_PID=" + pidFile}, own)
		return err, pidFile
	}
	alive := func(pidFile string) bool {
		b, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil {
			return false
		}
		return syscall.Kill(pid, 0) == nil
	}
	err, pidFile := run(t, time.Minute, 300*time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("own deadline: err = %v, want ErrTimeout", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for alive(pidFile) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if alive(pidFile) {
		t.Error("the forked child outlived the check")
	}
	err, _ = run(t, 300*time.Millisecond, time.Minute)
	if !errors.Is(err, ErrCancelled) || strings.Contains(err.Error(), "10 s") {
		t.Errorf("caller's deadline: err = %v, want ErrCancelled", err)
	}
}

// TestExpiryIgnoresImplausibleValues proves loginExpiresAt is used only when
// it is a plausible future time: an epoch before 2000 (a seconds/ms mix-up
// or a zero), and a time already past, read as unknown, so the guard never
// prints a misleading date.
func TestExpiryIgnoresImplausibleValues(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	for raw, want := range map[string]bool{
		`"2026-11-24T12:00:00Z"`: true,
		`1795521600000`:          true,
		`"2026-11-01T00:00:00Z"`: false,
		`"1970-01-02T00:00:00Z"`: false,
		`86400`:                  false,
		`5`:                      false,
		`0`:                      false,
		`"soon"`:                 false,
	} {
		if got := !expiryOf([]byte(raw), now).IsZero(); got != want {
			t.Errorf("expiryOf(%s) known = %v, want %v", raw, got, want)
		}
	}
}
