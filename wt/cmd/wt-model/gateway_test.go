package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestGatewayShowsTheHostSettings pins #693's read half: `wt-model gateway`
// prints the host's [gateway] state from the wt-model home, and the one fact a
// reader needs next to it: serve reads it once, at start. A home with no
// [gateway] table says off and plain http, and a machine with no home at all
// is not an error.
func TestGatewayShowsTheHostSettings(t *testing.T) {
	asWTModel(t)
	seedProxyHome(t, "http://127.0.0.1:4000", "sk-test", 0o600)
	out, _, err := execWTModel(t, testHost(), "gateway")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"subscription passthrough: off", "tls: off (plain http)", "serve reads [gateway] once, at start"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	enablePassthrough(t)
	cert, key := filepath.Join(t.TempDir(), "c.pem"), filepath.Join(t.TempDir(), "k.pem")
	home := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wt-model", "config.toml")
	f, err := os.OpenFile(home, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("tls_cert = \"" + cert + "\"\ntls_key = \"" + key + "\"\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	out, _, err = execWTModel(t, testHost(), "gateway")
	if err != nil || !strings.Contains(out, "subscription passthrough: on") || !strings.Contains(out, "tls: "+cert) {
		t.Errorf("with the switch on and a pair: %v\n%s", err, out)
	}
	if strings.Contains(out, "sk-test") {
		t.Errorf("the output shows the proxy key:\n%s", out)
	}
}

// TestGatewaySubscriptionWritesTheSwitch pins the write half: on and off
// change [gateway] subscription_passthrough alone (the proxy pair and the TLS
// pair stay), name the backup, and say serve must be restarted; the same value
// again says there is nothing to do and writes nothing; --dry-run changes
// nothing; a word other than on or off is a usage error.
func TestGatewaySubscriptionWritesTheSwitch(t *testing.T) {
	asWTModel(t)
	seedProxyHome(t, "http://127.0.0.1:4000", "sk-test", 0o600)
	home := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wt-model", "config.toml")
	before, _ := os.ReadFile(home)

	out, _, err := execWTModel(t, testHost(), "gateway", "subscription", "on", "--dry-run")
	if err != nil || !strings.Contains(out, "would turn subscription passthrough on") || !strings.Contains(out, "dry run: nothing was changed") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(home); string(got) != string(before) {
		t.Fatal("a dry run wrote the file")
	}

	out, _, err = execWTModel(t, testHost(), "gateway", "subscription", "on")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"subscription passthrough: on", "backup: ", "restart wt-model serve", "hazard"} {
		if !strings.Contains(out, want) {
			t.Errorf("on: output lacks %q:\n%s", want, out)
		}
	}
	if gw, _, err := config.GatewaySettings(); err != nil || !gw.SubscriptionPassthrough {
		t.Errorf("the switch did not read back as on: %+v, %v", gw, err)
	}
	if pair, err := config.ResolveProxy(); err != nil || pair.URL != "http://127.0.0.1:4000" {
		t.Errorf("the proxy pair changed: %+v, %v", pair, err)
	}

	out, _, err = execWTModel(t, testHost(), "gateway", "subscription", "on")
	if err != nil || !strings.Contains(out, "already on; nothing was changed") || strings.Contains(out, "backup:") {
		t.Errorf("the same value again: %v\n%s", err, out)
	}

	out, _, err = execWTModel(t, testHost(), "gateway", "subscription", "off")
	if err != nil || !strings.Contains(out, "subscription passthrough: off") || !strings.Contains(out, "restart wt-model serve") {
		t.Errorf("off: %v\n%s", err, out)
	}
	if gw, _, _ := config.GatewaySettings(); gw.SubscriptionPassthrough {
		t.Error("the switch did not read back as off")
	}

	if _, _, err := execWTModel(t, testHost(), "gateway", "subscription", "maybe"); err == nil || !strings.Contains(err.Error(), "on or off") {
		t.Errorf("a word that is not on or off: err = %v", err)
	}
}

// TestGatewaySubscriptionRefusesAnUnreadableHome pins that a config.toml that
// does not parse is reported and left alone, with exit status 1.
func TestGatewaySubscriptionRefusesAnUnreadableHome(t *testing.T) {
	asWTModel(t)
	seedProxyHome(t, "http://127.0.0.1:4000", "sk-test", 0o600)
	home := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wt-model", "config.toml")
	const bad = "[gateway\n"
	if err := os.WriteFile(home, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := execWTModel(t, testHost(), "gateway", "subscription", "on"); err == nil {
		t.Fatal("an unreadable home was written over")
	}
	if got, _ := os.ReadFile(home); string(got) != bad {
		t.Errorf("the refused write changed the file: %q", got)
	}
}

// TestGatewaySubscriptionWorksWithAnUnusableTLSPair pins that a TLS pair serve
// would refuse (a lone tls_cert) does not lock the operator out of the switch:
// the bare command still shows it and `subscription off` still writes it.
func TestGatewaySubscriptionWorksWithAnUnusableTLSPair(t *testing.T) {
	asWTModel(t)
	seedProxyHome(t, "http://127.0.0.1:4000", "sk-test", 0o600)
	enablePassthrough(t)
	home := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wt-model", "config.toml")
	f, err := os.OpenFile(home, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("tls_cert = \"/c.pem\"\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	out, _, err := execWTModel(t, testHost(), "gateway")
	if err != nil || !strings.Contains(out, "subscription passthrough: on") || !strings.Contains(out, "serve would refuse to start") {
		t.Errorf("show: %v\n%s", err, out)
	}
	out, _, err = execWTModel(t, testHost(), "gateway", "subscription", "off")
	if err != nil || !strings.Contains(out, "subscription passthrough: off (was on)") {
		t.Errorf("off: %v\n%s", err, out)
	}
}

// TestGatewayRestartHintIsBootoutAndBootstrap pins #693's restart wording:
// serve must be started afresh to re-read [gateway], so every place that says
// how names a bootout and a bootstrap (x then s in setup, or the launchctl
// commands) and none recommends a kickstart. Show, the write and the help all
// say it.
func TestGatewayRestartHintIsBootoutAndBootstrap(t *testing.T) {
	asWTModel(t)
	seedProxyHome(t, "http://127.0.0.1:4000", "sk-test", 0o600)
	show, _, err := execWTModel(t, testHost(), "gateway")
	if err != nil {
		t.Fatal(err)
	}
	wrote, _, err := execWTModel(t, testHost(), "gateway", "subscription", "on")
	if err != nil {
		t.Fatal(err)
	}
	help1, _, _ := execWTModel(t, testHost(), "gateway", "--help")
	help2, _, _ := execWTModel(t, testHost(), "gateway", "subscription", "--help")
	for name, out := range map[string]string{"show": show, "write": wrote, "gateway help": help1} {
		if !strings.Contains(out, "bootout") || !strings.Contains(out, "bootstrap") {
			t.Errorf("%s lacks bootout and bootstrap:\n%s", name, out)
		}
		if strings.Contains(noKickstartAdvice(out), "kickstart") {
			t.Errorf("%s recommends kickstart:\n%s", name, out)
		}
	}
	if strings.Contains(noKickstartAdvice(help2), "kickstart") {
		t.Errorf("subscription help mentions kickstart:\n%s", help2)
	}
	if !strings.Contains(wrote, "x then s") || !strings.Contains(wrote, "local.wt-model.serve") {
		t.Errorf("the write's hint lacks the setup keys or the job:\n%s", wrote)
	}
}

// noKickstartAdvice drops the two phrases that name a kickstart only to rule
// it out, so what is left must not mention one.
func noKickstartAdvice(s string) string {
	s = strings.ReplaceAll(s, "A kickstart does not do it.", "")
	return strings.ReplaceAll(s, "not a kickstart", "")
}
