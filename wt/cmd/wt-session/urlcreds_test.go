package main

import (
	"context"
	"errors"
	"fmt"
	neturl "net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
)

// sessionHostileAtURLs is internal/modelclient's hostileAtURLs (a test file
// there, which this package cannot import): urls that each hold an ASCII '@',
// the bypasses of every span-cutting design the old model-server tab tried, with
// distinctive user and password tokens no fixed text holds.
var sessionHostileAtURLs = []string{
	"u:SECRETPW@host:8787", "https://u:SECRETPW@host%zz", "https://u:SECRETPW@[::1",
	"//USERX:PASSX@hq", "///USERX:PASSX@hq", "USERX:p/PASSX@hq", "USERX:p?PASSX@hq",
	"USERX:p#PASSX@hq", "user9:p/ss9@h1", "user9:p?ss9@h1", "user9:p#ss9@h1",
	"http:/USERX:PASSX@hq", "http:///USERX:PASSX@hq",
	"http//USERX:PASSX@hq", "1http://USERX:PASSX@hq", "http:/\\USERX:PASSX@hq",
	"http:/ /USERX:PASSX@hq",
	"admin:s3cr://etQ@gpu.example:8787", "admin:hunter2://@gpu.example:8787",
	"USERX:PASSX://@hq", " USERX:PASSX://@hq", "USERX2:PASSX2://@hq", "uq:p://q@h1",
	"USERX:PASSX@gpu.example:8787", "https://USERX:PASSX@gpu.example:8787",
	"https://USERX@gpu.example:8787", "USERX@hostq", "USERX:@hostq", "u:p@[::1]:8787",
	"USERX:PASSX@[::1]:8787", "http://USERX:PASSX@[::1]:8787/",
	"@", "@@", "@hostq", "USERX@", "USERX:PASSX@", "@USERX:PASSX",
	"USERX:PASSX@hostq@other9", "USERX@PASSX@hostq", "a1@b2@c3@d4",
	"USERX:PASS\tX@hostq", "USERX:PASSX\n@hostq", "USERX\n:PASSX@hostq\n",
	"USERX:PASSX%40enc@hostq", "USERX%3APASSX@hostq", "http://USERX:PASSX%40w@hostq",
	"http://USERX:p@PASSX/x@gpu.example", "USERX:p@PASSX/x@gpu.example:8787",
	"https://USERX@PASSX?q@hostq", "USERX:PASSX@hostq:8787#fr@other9",
	"http://hostq/path?x=a@b9", "hostq:8787/path?x=USERX@PASSX", "https://hostq:8787/a@b9",
	"\\\\USERX:PASSX@hostq", "http:\\\\USERX:PASSX@hostq", "smbq:///USERX:PASSX@hostq",
	"ftp://USERX:PASSX@hostq", "HTTP://USERX:PASSX@HOSTQ", "javascript://USERX:PASSX@hostq",
	"http://USERX:PASSX@hostq?next=http://vq9:wq9@other9", "http://aq9/bq9://cq9@d19",
	"aq9@bq9://cq9@d19", "USERX:PASSX@@hostq", "USERX:PASSX@:8787", "USERX:PASSX@/zz9",
	"http://[USERX:PASSX@::1]:8787", "admin:s3cr://etQ@gpu.example:8787/v1?k=v#f",
	"USERX:\xff\xfePASSX@hostq", "\xffUSERX@\xfe", "USERX:PASSX@host q",
	"https://USERX:" + strings.Repeat("LONGPASSX", 400) + "@hostq:8787",
	"https://USERX:" + "PASSX@" + strings.Repeat("longhostq.", 300) + "example",
	"http://pw/@h", "SeCrEt://u:p@x", "http://%C3%A9%C3%A9/@x",
}

// sessionAtURLs is sessionHostileAtURLs plus '@' urls naming the fake
// server f, so a request built from one would reach it.
func sessionAtURLs(f *fakeModelServer) []string {
	addr := strings.TrimPrefix(f.URL, "http://")
	return append([]string{
		"http://USERX:PASSX@" + addr, "USERX:PASSX@" + addr, "http://USERX@" + addr + "/",
		"http://" + addr + "/?k=USERX@PASSX", "http://hunter2:@" + addr,
	}, sessionHostileAtURLs...)
}

// atURLFixedTexts are the fixed sentences an '@' url may be replaced by;
// they are taken out of an output before it is searched.
var atURLFixedTexts = []string{(&modelclient.CredentialsInURLError{}).Error(), modelclient.HiddenURL}

// atURLLeak returns the first piece of raw (split on url punctuation and
// white space, two bytes or longer; compared ignoring case and also
// percent-decoded) that out shows once the fixed texts are taken out, or "".
func atURLLeak(raw, out string) string {
	for _, fixed := range atURLFixedTexts {
		out = strings.ReplaceAll(out, fixed, "")
	}
	lower := strings.ToLower(out)
	for _, tok := range strings.FieldsFunc(raw, func(r rune) bool {
		return strings.ContainsRune(":/?#@\\", r) || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	}) {
		if len(tok) < 2 {
			continue
		}
		forms := []string{tok}
		if dec, err := neturl.PathUnescape(tok); err == nil && dec != tok && len(dec) >= 2 {
			forms = append(forms, dec)
		}
		for _, f := range forms {
			if strings.Contains(lower, strings.ToLower(f)) {
				return f
			}
		}
	}
	return ""
}

// atURLSource puts raw where a session reads its url: "env" sets
// WT_MODEL_URL and WT_MODEL_TOKEN (no config.toml), "config" writes
// [model-server] mode = "remote", url and a 0600 token_file. It reports
// false for a url TOML cannot hold (invalid UTF-8), which only env can carry.
func atURLSource(t *testing.T, f *fakeModelServer, source, raw string) bool {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	dir := filepath.Join(home, "agent-wt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "default_tag = \"code\"\n"
	switch source {
	case "env":
		t.Setenv("WT_MODEL_URL", raw)
		t.Setenv("WT_MODEL_TOKEN", f.Token)
	case "config":
		if !utf8.ValidString(raw) {
			return false
		}
		tok := filepath.Join(home, "token")
		if err := os.WriteFile(tok, []byte(f.Token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		body += fmt.Sprintf("\n[model-server]\nmode = \"remote\"\nurl = %s\ntoken_file = %s\n", strconv.Quote(raw), strconv.Quote(tok))
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return true
}

// TestNoPieceOfAnAtURLReachesSessionOutput is issue #639's property on the
// session side: for every url holding an '@' (at least 40, from WT_MODEL_URL
// or from url under [model-server]), resolveModels and openModels refuse with
// the fixed *modelclient.CredentialsInURLError (a hard refusal, not a "no
// server" case), `wt job` and `wt model list --server` exit 1, no request
// reaches the fake server a url may name, and no piece of the url is in any
// error, stdout or stderr. Before the fix a schemeless url such as
// u:SECRETPW@host:8787 was pinged and its "not reachable" error and
// --listen hint printed the password.
func TestNoPieceOfAnAtURLReachesSessionOutput(t *testing.T) {
	if len(sessionHostileAtURLs) < 40 {
		t.Fatalf("only %d hostile urls", len(sessionHostileAtURLs))
	}
	f := newFakeModelServer(t)
	for _, raw := range sessionAtURLs(f) {
		if !strings.Contains(raw, "@") {
			t.Fatalf("hostile url %q has no '@'", raw)
		}
		for _, source := range []string{"env", "config"} {
			t.Run(source, func(t *testing.T) {
				if !atURLSource(t, f, source, raw) {
					return
				}
				outs := map[string]string{}
				_, err := (&app{hooks: testHooks()}).resolveModels()
				var ce *modelclient.CredentialsInURLError
				if !errors.As(err, &ce) || modelclient.NoServer(err) {
					t.Errorf("resolveModels(%q) = %v, want the hard *CredentialsInURLError", raw, err)
				}
				if err != nil {
					outs["resolveModels"] = err.Error()
				}
				_, err = (&app{hooks: testHooks()}).openModels(context.Background(), "nothing was launched")
				if !errors.As(err, &ce) {
					t.Errorf("openModels(%q) = %v, want *CredentialsInURLError", raw, err)
				}
				if err != nil {
					outs["openModels"] = err.Error()
				}
				for _, args := range [][]string{{"job", "jq7k"}, {"job", "jq7k", "--json"}, {"model", "list", "--server"}} {
					var out string
					var code int
					notes := captureNotes(t)
					errOut := captureFile(t, &os.Stderr, func() {
						out += captureFile(t, &os.Stdout, func() {
							var o string
							o, code = runRootWith(t, testHooks(), args...)
							out += o
						})
					})
					if code != 1 {
						t.Errorf("wt %s with %q: exit %d, want 1\n%s", strings.Join(args, " "), raw, code, out+errOut)
					}
					outs["wt "+strings.Join(args, " ")] = out + errOut + notes.String()
				}
				for name, got := range outs {
					if p := atURLLeak(raw, got); p != "" {
						t.Errorf("%s for %q shows %q:\n%s", name, raw, p, got)
					}
				}
			})
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.Reqs) != 0 {
		t.Errorf("requests reached the fake server: %v", f.Reqs)
	}
}

// TestPingHintNeverShowsAnAtURL pins the remote hint's url (displayURL) on
// its own, as defence in depth behind ResolveEndpoint's refusal: a url
// holding an '@' is shown as the fixed placeholder whole, however it parses
// (a parse-based strip of the userinfo leaves "pw" of http://pw/@h), and a
// plain url keeps its host.
func TestPingHintNeverShowsAnAtURL(t *testing.T) {
	f := newFakeModelServer(t)
	for _, raw := range sessionAtURLs(f) {
		ms := &modelSession{mode: modelclient.ModeRemote, ep: modelclient.Endpoint{URL: raw, Token: "t"}}
		got := pingHint(errors.New("down"), ms).Error()
		if !strings.Contains(got, modelclient.HiddenURL) {
			t.Errorf("pingHint for %q = %q, want the placeholder", raw, got)
		}
		if p := atURLLeak(raw, got); p != "" {
			t.Errorf("pingHint for %q shows %q: %s", raw, p, got)
		}
	}
	if got := displayURL("http://gpu.example:8787/?k=v"); got != "http://gpu.example:8787/" {
		t.Errorf("displayURL of a plain url = %q", got)
	}
}

// TestPlainURLsStillResolve pins that the refusal touches only '@' urls: a
// plain url from config or the environment, schemeless or IPv6 included,
// resolves to an endpoint holding it unchanged.
func TestPlainURLsStillResolve(t *testing.T) {
	f := newFakeModelServer(t)
	for _, raw := range []string{"gpu.example:8787", "https://gpu.example:8787", "http://127.0.0.1:8787/", "[::1]:8787"} {
		for _, source := range []string{"env", "config"} {
			atURLSource(t, f, source, raw)
			ms, err := (&app{hooks: testHooks()}).resolveModels()
			if err != nil || ms.ep.URL != raw {
				t.Errorf("%s %q: resolveModels = %+v, %v; want the url unchanged", source, raw, ms, err)
			}
			t.Setenv("WT_MODEL_URL", "")
			t.Setenv("WT_MODEL_TOKEN", "")
		}
	}
}

// TestExplicitNativeIgnoresAnAtURL pins that native mode still ignores a
// stray url, an '@' one included: native resolves no endpoint, so the
// credentials refusal never fires and the agent launches natively (exit 0),
// whether native is stored with the url beside it or chosen by
// WT_MODEL_MODE over an '@' WT_MODEL_URL; nothing of the url is printed.
func TestExplicitNativeIgnoresAnAtURL(t *testing.T) {
	const raw = "u:SECRETPW@host:8787"
	t.Run("stored", func(t *testing.T) {
		r := storedNativeHome(t)
		r.appendConfig("url = " + strconv.Quote(raw) + "\n")
		out, code := r.run(t, "--cwd", "-A", "codex")
		if code != 0 || !r.ranAgent("codex") || len(r.builds) != 1 || !r.builds[0].model.Native {
			t.Fatalf("exit %d, builds %+v; want one native launch\n%s", code, r.builds, out)
		}
		if strings.Contains(out, "SECRETPW") {
			t.Errorf("output shows the password:\n%s", out)
		}
	})
	t.Run("env", func(t *testing.T) {
		r := newNativeRig(t)
		r.chooseNative()
		t.Setenv("WT_MODEL_URL", raw)
		t.Setenv("WT_MODEL_TOKEN", "t")
		out, code := r.run(t, "--cwd", "-A", "codex")
		if code != 0 || !r.ranAgent("codex") || len(r.builds) != 1 || !r.builds[0].model.Native {
			t.Fatalf("exit %d, builds %+v; want one native launch\n%s", code, r.builds, out)
		}
		if strings.Contains(out, "SECRETPW") {
			t.Errorf("output shows the password:\n%s", out)
		}
	})
}
