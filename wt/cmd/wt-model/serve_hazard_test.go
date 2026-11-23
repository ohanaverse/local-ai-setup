package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// pingServe asks the running serve at addr for its ping (token tok-abc).
func pingServe(t *testing.T, addr string) modelapi.Ping {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), "GET", "http://"+addr+"/control/v1/ping", nil)
	req.Header.Set("Authorization", "Bearer tok-abc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var ping modelapi.Ping
	if err := json.NewDecoder(resp.Body).Decode(&ping); err != nil || resp.StatusCode != 200 {
		t.Fatalf("ping = %d, %v", resp.StatusCode, err)
	}
	return ping
}

// warningLines is the "wt-model: warning:" lines of out.
func warningLines(out string) []string {
	var ws []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "wt-model: warning: ") {
			ws = append(ws, l)
		}
	}
	return ws
}

// TestServeKeepsModeOffOnHazard pins serve's start-time hazard check
// (decision 5): with the mode switched on and the proxy listing a foreign
// anthropic/ deployment (LiteLLM would forward a client's login there), serve
// keeps the mode off, prints one warning naming the hazard but no api_base
// value, prints no "passthrough is on" line, and its ping says off. A proxy
// that cannot be read (it may still be starting at login) is one warning and
// the mode stays on, because the sync gates routing; serve still listens.
// The bound itself is pinned on subscriptionGate with a reader that blocks.
func TestServeKeepsModeOffOnHazard(t *testing.T) {
	asWTModel(t)
	t.Run("foreign anthropic deployment", func(t *testing.T) {
		seedProxyHome(t, "http://127.0.0.1:4000", "sk-test", 0o600)
		enablePassthrough(t)
		h := testHost()
		h.ModelInfo = func(context.Context) ([]litellm.DeploymentInfo, error) {
			return []litellm.DeploymentInfo{{Provider: "anthropic", ModelName: "hand-claude", Model: "anthropic/claude-x", APIBase: "https://relay.example"}}, nil
		}
		read, _, stop := serveUntilListeningOn(t, h, testRuntime(t), writeToken(t, 0o600))
		ping := pingServe(t, listeningAddr(t, read()))
		if err := stop(); err != nil {
			t.Fatalf("serve returned %v", err)
		}
		out := read()
		if ping.SubscriptionPassthrough {
			t.Errorf("ping reports the mode on despite the hazard")
		}
		ws := warningLines(out)
		if len(ws) != 1 || !strings.Contains(ws[0], "hand-claude") || !strings.Contains(ws[0], "subscription passthrough") {
			t.Errorf("warnings = %q, want one naming the hazard", ws)
		}
		if strings.Contains(out, subInfo) || strings.Contains(out, "relay.example") {
			t.Errorf("stderr says the mode is on or quotes the api_base:\n%s", out)
		}
	})
	t.Run("proxy does not answer", func(t *testing.T) {
		seedProxyHome(t, "http://127.0.0.1:4000", "sk-test", 0o600)
		enablePassthrough(t)
		h := testHost()
		h.ModelInfo = func(context.Context) ([]litellm.DeploymentInfo, error) { return nil, context.DeadlineExceeded }
		read, _, stop := serveUntilListeningOn(t, h, testRuntime(t), writeToken(t, 0o600))
		ping := pingServe(t, listeningAddr(t, read()))
		if err := stop(); err != nil {
			t.Fatalf("serve returned %v", err)
		}
		out := read()
		if !ping.SubscriptionPassthrough || !strings.Contains(out, subInfo) {
			t.Errorf("mode not on (ping %v):\n%s", ping.SubscriptionPassthrough, out)
		}
		if ws := warningLines(out); len(ws) != 1 {
			t.Errorf("warnings = %q, want exactly one", ws)
		}
	})
	t.Run("bound", func(t *testing.T) {
		block := func(ctx context.Context) ([]litellm.DeploymentInfo, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		start := time.Now()
		on, line := subscriptionGate(t.Context(), "", litellm.ProxyEnvOf("test env", nil), block, 50*time.Millisecond)
		if !on || !strings.HasPrefix(line, "wt-model: warning: ") || time.Since(start) > 5*time.Second {
			t.Errorf("subscriptionGate = %v, %q after %s; want on, one warning, within the bound", on, line, time.Since(start))
		}
		on, line = subscriptionGate(t.Context(), "", litellm.ProxyEnvOf("test env", map[string]string{"ANTHROPIC_API_KEY": "wtSENTINEL"}), block, 50*time.Millisecond)
		if on || !strings.Contains(line, "ANTHROPIC_API_KEY") || strings.Contains(line, "SENTINEL") {
			t.Errorf("env hazard with an unreadable proxy = %v, %q; want off, naming the variable", on, line)
		}
	})
}
