package litellm

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const proxyPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>local.litellm.proxy</string>
	<key>ProgramArguments</key>
	<array>
		<string>/bin/litellm</string>
		<dict><key>EnvironmentVariables</key><string>a decoy inside an array</string></dict>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>IN_PLIST</key>
		<string>http://box:11434</string>
		<key>EMPTY_IN_PLIST</key>
		<string></string>
	</dict>
	<key>KeepAlive</key>
	<true/>
</dict>
</plist>
`

// writePlist writes body as the proxy LaunchAgent plist for this test and
// points wt at it, with the default restart (launchd) in force.
func writePlist(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "local.litellm.proxy.plist")
	if body != "" {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("WT_LITELLM_PLIST", p)
	t.Setenv("WT_LITELLM_RESTART_CMD", "")
	t.Setenv("MODELMAN_LITELLM_RESTART_CMD", "")
	return p
}

// TestProxyEnvReadsTheLaunchAgent pins whose environment wt consults when it
// asks "is this variable set for the proxy?" (#211). The proxy runs under its
// LaunchAgent, which gets the plist's EnvironmentVariables and not the shell
// wt was typed in — so a variable only wt's shell has is NOT set for the
// proxy, and one only the plist has IS. Checking wt's own environment would be
// wrong both ways. A variable set to the empty string is set.
func TestProxyEnvReadsTheLaunchAgent(t *testing.T) {
	p := writePlist(t, proxyPlist)
	t.Setenv("ONLY_IN_SHELL", "x")
	env := LoadProxyEnv()
	for name, want := range map[string]bool{"IN_PLIST": true, "EMPTY_IN_PLIST": true, "ONLY_IN_SHELL": false, "NOWHERE": false, "Label": false} {
		if got := env.IsSet(name); got != want {
			t.Errorf("IsSet(%q) = %v, want %v", name, got, want)
		}
	}
	if !strings.Contains(env.Source, p) {
		t.Errorf("Source = %q, want it to name the plist %s", env.Source, p)
	}
}

// TestProxyEnvFallsBackToWtsEnvironment pins the fallback: when the plist is
// not what starts the proxy (a restart command is configured), or cannot be
// read (missing, binary, malformed), wt's own environment is the best answer
// left — and Source says so, so the warning does not claim to have read a file
// it did not. A plist that is readable but has no EnvironmentVariables is an
// answer, not a failure: nothing is set for the proxy.
func TestProxyEnvFallsBackToWtsEnvironment(t *testing.T) {
	t.Setenv("ONLY_IN_SHELL", "x")
	ambient := func(label string) {
		t.Helper()
		env := LoadProxyEnv()
		if !env.IsSet("ONLY_IN_SHELL") || env.IsSet("IN_PLIST") || !strings.Contains(env.Source, "wt's own environment") {
			t.Errorf("%s: shell var set=%v plist var set=%v source=%q, want wt's own environment", label, env.IsSet("ONLY_IN_SHELL"), env.IsSet("IN_PLIST"), env.Source)
		}
	}
	writePlist(t, proxyPlist)
	t.Setenv("WT_LITELLM_RESTART_CMD", "systemctl restart litellm")
	ambient("a restart command is configured")

	writePlist(t, "")
	ambient("no plist")
	writePlist(t, "bplist00\x01\x02binary")
	ambient("binary plist")
	writePlist(t, "<plist><dict><key>EnvironmentVariables</key><dict><key>IN_PLIST</key>")
	ambient("truncated plist")

	writePlist(t, `<plist version="1.0"><dict><key>Label</key><string>x</string></dict></plist>`)
	if env := LoadProxyEnv(); env.IsSet("ONLY_IN_SHELL") || strings.Contains(env.Source, "wt's own environment") {
		t.Errorf("readable plist with no EnvironmentVariables: shell var set=%v source=%q, want the plist's (empty) answer", env.IsSet("ONLY_IN_SHELL"), env.Source)
	}
}

// environBody holds rows whose api_base is an os.environ/ reference. LiteLLM
// resolves those before it decides whether to start its own `ollama serve`,
// and an unset variable resolves to None — the same as no api_base at all.
const environBody = `model_list:
  - model_name: hand/unset
    litellm_params:
      model: openai/ollama-proxy
      api_base: os.environ/OLLAMA_BASE_UNSET
  - model_name: hand/set
    litellm_params: {model: openai/ollama-proxy, api_base: os.environ/IN_PLIST}
  - model_name: hand/empty
    litellm_params: {model: openai/ollama-proxy, api_base: os.environ/EMPTY_IN_PLIST}
  - model_name: ollama/unset
    litellm_params: {model: ollama_chat/far:1b, api_base: os.environ/OLLAMA_BASE_UNSET}
  - model_name: keep/me
    litellm_params: {model: openai/gpt-4o, api_base: os.environ/OLLAMA_BASE_UNSET}
litellm_settings:
  drop_params: true
  use_chat_completions_url_for_anthropic_messages: true
`

// TestSyncWarnsAboutAnUnsetEnvironAPIBase pins #211. A row whose api_base is
// `os.environ/VAR` looked like it named an address, so wt neither repaired it
// nor warned — but with VAR unset for the proxy LiteLLM sees no api_base and
// starts the second `ollama serve` of #202. Sync and its dry run now name the
// row and the variable, and say where they looked. A variable that is set for
// the proxy (even to "") is fine, a row that does not name ollama is not
// LiteLLM's trigger, and no row is rewritten: a variable may point somewhere
// else on purpose, so even an ollama/ row keeps its reference.
func TestSyncWarnsAboutAnUnsetEnvironAPIBase(t *testing.T) {
	plist := writePlist(t, proxyPlist)
	t.Setenv("OLLAMA_BASE_UNSET", "http://set-only-in-wts-shell:11434")
	cfg := testConfig()
	const tail = `: LiteLLM starts its own "ollama serve" for it at proxy startup; set OLLAMA_BASE_UNSET for the proxy or give the row an address`
	where := "the proxy LaunchAgent's EnvironmentVariables (" + plist + ")"
	want := []string{
		`row "hand/unset" (model openai/ollama-proxy) has api_base os.environ/OLLAMA_BASE_UNSET, and OLLAMA_BASE_UNSET is not set in ` + where + tail,
		`row "ollama/unset" (model ollama_chat/far:1b) has api_base os.environ/OLLAMA_BASE_UNSET, and OLLAMA_BASE_UNSET is not set in ` + where + tail,
	}
	o, _, p := opts(t, environBody)
	plan, err := PlanSync(cfg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Warnings, want) || len(plan.Repair) != 0 {
		t.Errorf("dry run: warnings = %q repair = %v\nwant warnings %q and no repair", plan.Warnings, plan.Repair, want)
	}
	res, err := Sync(cfg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Warnings, want) {
		t.Errorf("sync warnings = %q, want %q", res.Warnings, want)
	}
	b, _ := os.ReadFile(p)
	if n := strings.Count(string(b), "os.environ/OLLAMA_BASE_UNSET"); n != 3 {
		t.Errorf("config holds the reference %d times, want all 3 kept — sync must not rewrite one:\n%s", n, b)
	}
}
