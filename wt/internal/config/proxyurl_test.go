package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// atProxyURLs are LiteLLM pair urls holding an ASCII '@' anywhere (issue
// #640): with and without a scheme, a password holding '/', an '@' only in
// the query, a bare '@'. Each user or password part holds USERZ or PASSZ.
var atProxyURLs = []string{
	"USERZ:PASSZ@litellm:4000",
	"http://USERZ:PASSZ@localhost:4000",
	"https://USERZ@proxy.example",
	"USERZ:p/PASSZ@hq",
	"//USERZ:PASSZ@hq",
	"http://localhost:4000/?k=USERZ@PASSZ",
	"@",
}

// plainProxyURLs carry no ASCII '@' (a fullwidth one and %40 are not '@').
var plainProxyURLs = []string{
	"http://localhost:4000",
	"http://127.0.0.1:4000/",
	"https://proxy.example:4443/v1",
	"http://h/u＠x",
	"http://h/u%40x",
	"",
}

// TestShownProxyURLHidesOnlyAnAtURL pins config's pair-url names as
// delegations of the one url rule (internal/urlrule): an '@' anywhere hides
// the whole url and flags it, a plain url passes through untouched, and the
// placeholder is the rule's fixed text. (The test that held these equal to
// modelclient's copy is gone with the copy: from PR 1b modelclient imports
// modelmode, which imports config, so this internal test may not import
// modelclient — the rule lives once in urlrule instead.)
func TestShownProxyURLHidesOnlyAnAtURL(t *testing.T) {
	if ProxyURLHidden != "(hidden: the url contains an @)" {
		t.Errorf("ProxyURLHidden = %q, want the rule's fixed placeholder", ProxyURLHidden)
	}
	for _, raw := range atProxyURLs {
		if !ProxyURLHasAt(raw) {
			t.Errorf("ProxyURLHasAt(%q) = false, want true", raw)
		}
		if got := ShownProxyURL(raw); got != ProxyURLHidden {
			t.Errorf("ShownProxyURL(%q) = %q, want %q", raw, got, ProxyURLHidden)
		}
	}
	for _, raw := range plainProxyURLs {
		if ProxyURLHasAt(raw) {
			t.Errorf("ProxyURLHasAt(%q) = true, want false", raw)
		}
		if got := ShownProxyURL(raw); got != raw {
			t.Errorf("ShownProxyURL(%q) = %q, want it unchanged", raw, got)
		}
	}
}

// TestSetProxyRefusesAnAtURL pins the write side of #640: a url holding an
// '@' is refused before anything is touched (no home config.toml, no key
// file, even with --api-key given beside it, and an existing pair stays
// byte-identical), with the fixed ErrProxyURLHasAt whose text quotes no
// piece of the url; a key-only set over such a stored url still works, so
// the refusal never locks the user out of the one command that repairs it.
func TestSetProxyRefusesAnAtURL(t *testing.T) {
	for _, raw := range atProxyURLs {
		home := proxyEnv(t, "")
		err := SetProxy(ptr(raw), ptr("sk-new"))
		if !errors.Is(err, ErrProxyURLHasAt) {
			t.Fatalf("%q: err = %v, want ErrProxyURLHasAt", raw, err)
		}
		if strings.Contains(err.Error(), "USERZ") || strings.Contains(err.Error(), "PASSZ") {
			t.Errorf("%q: the refusal quotes the url: %q", raw, err)
		}
		if _, serr := os.Stat(filepath.Join(home, "wt-model")); !os.IsNotExist(serr) {
			t.Errorf("%q: the wt-model home was created (%v)", raw, serr)
		}

		// Over an existing pair: nothing changes.
		if err := SetProxy(ptr("http://localhost:4000"), ptr("sk-old")); err != nil {
			t.Fatal(err)
		}
		cfg, key := ProxyHomePath(), filepath.Join(home, "wt-model", defaultProxyKeyFile)
		cfgBefore, _ := os.ReadFile(cfg)
		keyBefore, _ := os.ReadFile(key)
		if err := SetProxy(ptr(raw), ptr("sk-new")); !errors.Is(err, ErrProxyURLHasAt) {
			t.Errorf("%q over a pair: err = %v", raw, err)
		}
		cfgAfter, _ := os.ReadFile(cfg)
		keyAfter, _ := os.ReadFile(key)
		if !bytes.Equal(cfgBefore, cfgAfter) || !bytes.Equal(keyBefore, keyAfter) {
			t.Errorf("%q: a refused set changed the pair", raw)
		}
	}

	// A hand-written '@' url already stored: a key-only set still works.
	home := proxyEnv(t, "")
	writeProxyHome(t, home, "[litellm]\nurl = \"USERZ:PASSZ@hq\"\nkey_file = \"litellm.key\"\n")
	if err := SetProxy(nil, ptr("sk-new")); err != nil {
		t.Errorf("a key-only set over a stored '@' url: %v", err)
	}
}

// TestWriteProxyStatusHidesAnAtURL pins that `litellm status` (both
// binaries) never prints a stored pair url holding an '@': text and JSON
// show the fixed placeholder, whole, and nothing of the url; a plain url
// prints as before (TestWriteProxyStatusMatchesTheFixtureMember).
func TestWriteProxyStatusHidesAnAtURL(t *testing.T) {
	for _, raw := range atProxyURLs {
		for _, asJSON := range []bool{false, true} {
			var out bytes.Buffer
			if err := WriteProxyStatus(&out, ProxyPair{URL: raw, APIKey: "sk-host1234"}, asJSON); err != nil {
				t.Fatal(err)
			}
			s := out.String()
			if strings.Contains(s, "USERZ") || strings.Contains(s, "PASSZ") || !strings.Contains(s, ProxyURLHidden) {
				t.Errorf("%q json=%t: status = %q, want the placeholder and nothing of the url", raw, asJSON, s)
			}
		}
	}
}
