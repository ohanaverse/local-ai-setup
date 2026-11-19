package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// The tests below moved here from cmd/wt-session's model_cmds_test.go with
// `wt served` and `wt warm`, which are now `wt-model served` and
// `wt-model warm`.

// omlxServer is a keyed omlx with two models in its pool and B loaded: /health
// gives the counts to anyone, /v1/models/status says which only to the key.
func omlxServer(t *testing.T, key string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"engine_pool":{"model_count":2,"loaded_count":1}}`))
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"A"},{"id":"B"}]}`))
		case "/v1/models/status":
			if r.Header.Get("Authorization") != "Bearer "+key {
				http.Error(w, `{"detail":"Invalid API key"}`, http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"models":[{"id":"A","loaded":false},{"id":"B","loaded":true}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func omlxServedCfg(origin, secretRef string) *config.Config {
	return &config.Config{Providers: []config.Provider{{
		ID: "omlx", Location: config.LocationLocal,
		Auth: config.AuthConfig{Type: "api_key", SecretRef: secretRef, BaseURL: origin + "/v1"},
	}}}
}

// TestServedReportsWhatAKeyedOmlxHasLoaded pins the reason `wt-model served`
// exists: a partly loaded omlx pool names the loaded model only to a caller
// holding the server's key, and wt is the one that resolves the registry's
// secret_ref. A script whose own keyless probe is refused asks here.
func TestServedReportsWhatAKeyedOmlxHasLoaded(t *testing.T) {
	srv := omlxServer(t, "sk-omlx")
	cfg := omlxServedCfg(srv.URL, "sk-omlx")

	var out bytes.Buffer
	if err := runServed(&out, cfg, srv.Client(), "omlx", true); err != nil {
		t.Fatalf("runServed --json: %v", err)
	}
	if got, want := out.String(), `{"provider":"omlx","served":["B"]}`+"\n"; got != want {
		t.Errorf("json = %q, want %q", got, want)
	}

	out.Reset()
	if err := runServed(&out, cfg, srv.Client(), "omlx", false); err != nil {
		t.Fatalf("runServed: %v", err)
	}
	if got := out.String(); got != "B\n" {
		t.Errorf("plain = %q, want %q", got, "B\n")
	}
}

// TestRunServedSushiListsOnlyLoadedPacks pins `wt-model served sushi` for the real
// sushi family: sushi lists every pack it has, and only the loaded one is
// serving, so only that id is printed (plain, and as JSON); when none is
// loaded the JSON list is an empty array, never null, so a script reads
// "nothing serving" rather than a decode error. The server is an httptest
// listener the registry points at, never port 12345.
func TestRunServedSushiListsOnlyLoadedPacks(t *testing.T) {
	serve := func(body string) *config.Config {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
		t.Cleanup(srv.Close)
		return &config.Config{Providers: []config.Provider{{
			ID: "sushi", Location: config.LocationLocal,
			Auth: config.AuthConfig{Type: "none", BaseURL: srv.URL + "/v1"},
		}}}
	}
	mixed := serve(`{"data":[{"id":"p/a","loaded":false,"state":"unloaded"},{"id":"p/b","loaded":true,"state":"ready"},{"id":"p/c","loaded":false,"state":"loading"}]}`)
	none := serve(`{"data":[{"id":"p/a","loaded":false,"state":"unloaded"},{"id":"p/c","loaded":false,"state":"loading"}]}`)
	for _, tc := range []struct {
		name string
		cfg  *config.Config
		json bool
		want string
	}{
		{"plain, one loaded", mixed, false, "p/b\n"},
		{"json, one loaded", mixed, true, `{"provider":"sushi","served":["p/b"]}` + "\n"},
		{"json, none loaded", none, true, `{"provider":"sushi","served":[]}` + "\n"},
		{"plain, none loaded", none, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := runServed(&out, tc.cfg, http.DefaultClient, "sushi", tc.json); err != nil {
				t.Fatalf("runServed: %v", err)
			}
			if got := out.String(); got != tc.want {
				t.Errorf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestServedFailsRatherThanAnswerNothing: without the key the server will not
// say which model is loaded, and that must be an error — an empty list with
// exit 0 would read as "nothing is serving" and clear the flag of a model the
// server still has loaded.
func TestServedFailsRatherThanAnswerNothing(t *testing.T) {
	srv := omlxServer(t, "sk-omlx")

	var out bytes.Buffer
	err := runServed(&out, omlxServedCfg(srv.URL, ""), srv.Client(), "omlx", true)
	if err == nil || !strings.Contains(err.Error(), "omlx gave no usable answer") {
		t.Fatalf("err = %v, want a no-usable-answer error", err)
	}
	if out.Len() != 0 {
		t.Errorf("printed %q on a failed probe, want nothing", out.String())
	}
}

// TestServedRefusesOllama: ollama has no "serving now" — a pulled model loads
// on request — so it is refused by name rather than answered with its pulls.
func TestServedRefusesOllama(t *testing.T) {
	var out bytes.Buffer
	err := runServed(&out, &config.Config{}, http.DefaultClient, "ollama", false)
	if err == nil || !strings.HasPrefix(err.Error(), `unknown provider "ollama": wt-model served answers for `) {
		t.Fatalf("err = %v, want an unknown-provider error naming wt-model served", err)
	}
}

// TestWarmSendsTheRegistryKeyToAKeyedOmlx pins `wt-model warm`, the step llmbench's
// omlx backend asks for when omlx refuses its keyless warmup (#256): the chat
// request carries the key the registry's omlx provider names, and names the
// model by its directory basename.
func TestWarmSendsTheRegistryKeyToAKeyedOmlx(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-omlx" {
			http.Error(w, `{"error":{"message":"API key required"}}`, http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			gotAuth, gotBody = r.Header.Get("Authorization"), string(b)
			_, _ = w.Write([]byte(`{"object":"chat.completion"}`))
		}
	}))
	t.Cleanup(srv.Close)

	var out bytes.Buffer
	if err := runWarm(context.Background(), &out, omlxServedCfg(srv.URL, "sk-omlx"), "omlx", "org/Qwen-4bit"); err != nil {
		t.Fatalf("runWarm: %v", err)
	}
	if gotAuth != "Bearer sk-omlx" || !strings.Contains(gotBody, `"model":"Qwen-4bit"`) {
		t.Errorf("auth = %q body = %s", gotAuth, gotBody)
	}
	if got := out.String(); got != "org/Qwen-4bit is loaded on omlx\n" {
		t.Errorf("out = %q", got)
	}

	// Without the key the refusal is the error, with the setting to change.
	out.Reset()
	err := runWarm(context.Background(), &out, omlxServedCfg(srv.URL, ""), "omlx", "Qwen-4bit")
	if err == nil || !strings.Contains(err.Error(), "auth.secret_ref") {
		t.Fatalf("err = %v, want one naming auth.secret_ref", err)
	}
	if out.Len() != 0 {
		t.Errorf("printed %q on a refused warm, want nothing", out.String())
	}
}

// TestWarmRefusesOtherProviders: the other local servers take no key, so
// there is nothing wt can add to their caller's own warmup.
func TestWarmRefusesOtherProviders(t *testing.T) {
	var out bytes.Buffer
	err := runWarm(context.Background(), &out, &config.Config{}, "mtplx", "m")
	if want := `unknown provider "mtplx": wt-model warm loads models into omlx only`; err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}
