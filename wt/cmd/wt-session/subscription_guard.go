// The pre-launch guard of a launch that sends the user's own Claude login
// through the model server's gateway (#597, decisions 2, 3, 7, 8 and 12).
// Every launch path runs it after the catalog resolve and before the client
// session's register, any start job and any gateway call: the non-TUI
// single-model launch (launchFilteredRemote), the non-TUI slot launch
// (launchSlotProfileRemote), and both TUI launches through the
// tui.ProfileLayer.Guard closure main.go builds (tuiSubscriptionGuard).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/claudelogin"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelping"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
)

// guardStill is what every refusal of the guard adds: it runs before the
// client session and before any start.
const guardStill = " (nothing was started or registered)"

// loginExpiryNotice is how soon a login's expiry earns a note.
const loginExpiryNotice = 24 * time.Hour

// errLoginNotStubbed is the nil login hook's answer in a test binary: a
// test never runs the real claude (decision 8).
var errLoginNotStubbed = errors.New("claude login check not stubbed in a test")

// errClaudeNotFound is realClaudeLogin's answer when no claude is on PATH:
// the refusal then says to install it, not to log in.
var errClaudeNotFound = errors.New("claude is not on PATH")

// login is the claude.ai login check: the hook, else (outside a test binary)
// realClaudeLogin.
func (h hooks) login(ctx context.Context) (claudelogin.Status, error) {
	if h.claudeLogin != nil {
		return h.claudeLogin(ctx)
	}
	if testing.Testing() {
		return claudelogin.Status{}, errLoginNotStubbed
	}
	return realClaudeLogin(ctx)
}

// realClaudeLogin runs claudelogin.Check on the claude found on PATH, in
// this process's environment (Check scrubs the credentials out of it).
func realClaudeLogin(ctx context.Context) (claudelogin.Status, error) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		return claudelogin.Status{}, errClaudeNotFound
	}
	return claudelogin.Check(ctx, bin, os.Environ())
}

// subscriptionGuard refuses a launch that would send the user's Claude login
// where it must not go, or that cannot work. It returns nil when no row uses
// the subscription (catalog.UsesSubscription). Otherwise it checks, in this
// order: this machine opted in ([model-server] subscription, decision 12);
// the endpoint is https or loopback (subscriptionEndpointOK, decision 3, no
// request); the server's ping advertises the passthrough (decision 2; cached
// by the client); the launch profile names none of the keys header mode
// sets (decision 7); Claude Code is MinVersion or newer; and a claude.ai
// login is present (decision 8). The notes it prints (the host the login is
// sent to, a login expiring soon) go to osStderr.
func subscriptionGuard(ctx context.Context, ms *modelSession, rows []modelapi.Row, profileEnvKeys []string, login func(context.Context) (claudelogin.Status, error)) error {
	return subscriptionGuardTo(ctx, osStderr, ms, rows, profileEnvKeys, login)
}

// subscriptionGuardTo is subscriptionGuard writing its notes to w (the TUI
// queues them until the terminal is released).
func subscriptionGuardTo(ctx context.Context, w io.Writer, ms *modelSession, rows []modelapi.Row, profileEnvKeys []string, login func(context.Context) (claudelogin.Status, error)) error {
	var ids []string
	for _, r := range rows {
		if catalog.UsesSubscription(r) {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	what := strings.Join(ids, ", ")
	refuse := func(format string, args ...any) error {
		return fmt.Errorf("%s uses your Claude subscription, but "+format+guardStill, append([]any{what}, args...)...)
	}
	if ms == nil {
		return refuse("no model server is configured")
	}
	// 1. Opt-in.
	if !ms.subscription {
		return refuse("this machine has not opted in to sending its Claude login to a model server: set [model-server] subscription = true in ~/.config/agent-wt/config.toml")
	}
	// 2. Endpoint: decided from the url alone, before any request.
	if err := subscriptionEndpointOK(ms.ep); err != nil {
		return refuse("%v", err)
	}
	// 3. The server's passthrough, from the client's cached ping.
	p, err := ms.client.Ping(ctx)
	if err != nil {
		return refuse("the model server did not answer its ping (%v)", err)
	}
	// The TUI path never runs openModels: the tls_off note is said here
	// (once per session, whichever reads the ping first).
	ms.noteTLSOff(w, p)
	if !p.SubscriptionPassthrough {
		return refuse("%s", subscriptionOffReason(ms))
	}
	// 4. The launch profile, and an inherited switch that turns Node's
	// certificate check of the gateway off (named, never its value).
	if agents.NodeTLSVerifyOff(os.Getenv(agents.NodeTLSRejectVar)) {
		return refuse("%s is set in this environment to turn off certificate checks, so your Claude login could reach a server posing as the gateway: unset it", agents.NodeTLSRejectVar)
	}
	for _, k := range profileEnvKeys {
		if agents.HeaderModeRefusedKey(k) {
			return refuse("the profile for this launch sets %s, which would replace the header wt sends or displace your Claude login: remove it from that profile", k)
		}
	}
	// 5 and 6. Claude Code's version, then the login.
	if login == nil {
		return refuse("no claude.ai login was found: run 'claude auth login'")
	}
	st, lerr := login(ctx)
	if errors.Is(lerr, errClaudeNotFound) {
		return refuse("Claude Code (claude) was not found on PATH: install it or put it on PATH")
	}
	if st.Version == "" && lerr != nil {
		return refuse("no claude.ai login was found (%v): run 'claude auth login'", lerr)
	}
	if !st.VersionAtLeast(claudelogin.MinVersion) {
		found := st.Version
		if found == "" {
			found = "an unreadable version"
		}
		return refuse("it needs Claude Code %s or newer and found %s: update claude", claudelogin.MinVersion, found)
	}
	if lerr != nil || !st.OK() {
		return refuse("no claude.ai login was found: run 'claude auth login'")
	}
	if !st.ExpiresAt.IsZero() && time.Until(st.ExpiresAt) < loginExpiryNotice {
		fmt.Fprintf(w, "wt: note: your claude.ai login expires at %s; run 'claude auth login' if the session outlasts it\n", st.ExpiresAt.Local().Format("2006-01-02 15:04"))
	}
	if host, local := endpointHost(ms.ep); !local {
		fmt.Fprintf(w, "wt: note: your Claude login will be sent to the model server at %s\n", host)
	}
	return nil
}

// subscriptionOffReason is decision 2's one message for a server whose ping
// lacks subscription_passthrough: off and older look the same.
func subscriptionOffReason(ms *modelSession) string {
	return modelping.SubscriptionOffReason(ms.ep.URL)
}

// subscriptionBlockedReason is what a blocked subscription row (or a slot
// profile blocked with one among its slots) says when the server's cached
// ping lacks the passthrough: decision 2's message, not the row's own block
// reason (an older server marks the row unmapped). "" when it does not apply.
func subscriptionBlockedReason(ctx context.Context, ms *modelSession, rows []modelapi.Row) string {
	if ms == nil || !slices.ContainsFunc(rows, catalog.UsesSubscription) {
		return ""
	}
	p, err := ms.client.Ping(ctx)
	if err != nil || p.SubscriptionPassthrough {
		return ""
	}
	var ids []string
	for _, r := range rows {
		if catalog.UsesSubscription(r) {
			ids = append(ids, r.ID)
		}
	}
	return strings.Join(ids, ", ") + " uses your Claude subscription, but " + subscriptionOffReason(ms)
}

// subscriptionEndpointOK is the client half of decision 3, decided from the
// url alone (it never dials): https to any host passes (its certificate is
// verified by the client); plain http passes only to a loopback host; any
// other scheme, or a url with no host, is refused. The error names the
// host:port, never the url (modelping.Addr).
func subscriptionEndpointOK(ep modelclient.Endpoint) error {
	u, err := url.Parse(ep.Origin())
	if err != nil || u.Host == "" {
		return errors.New(modelping.UnbuildableRequest)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if loopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("the model server at %s is plain http to a host that is not loopback, so your Claude login would cross the network unencrypted: use an https url (native TLS) or a tunnel that arrives on loopback", modelping.Addr(ep.URL))
	}
	return fmt.Errorf("the model server at %s is not an http or https url", modelping.Addr(ep.URL))
}

// loopbackHost reports a loopback host: the name localhost (a trailing dot
// allowed), exactly, or an address in 127.0.0.0/8 or ::1 after unmapping
// (::ffff:127.x counts). 0.0.0.0, 127.1 and every other spelling are not.
func loopbackHost(h string) bool {
	if h == "localhost" || h == "localhost." {
		return true
	}
	a, err := netip.ParseAddr(h)
	if err != nil {
		return false
	}
	a = a.Unmap().WithZone("")
	return (a.Is4() && a.As4()[0] == 127) || a == netip.IPv6Loopback()
}

// endpointHost is the host the login goes to, and whether it is loopback.
func endpointHost(ep modelclient.Endpoint) (string, bool) {
	u, err := url.Parse(ep.Origin())
	if err != nil || u.Host == "" {
		return modelping.UnparsedAddr, false
	}
	return u.Hostname(), loopbackHost(u.Hostname())
}

// launchProfileKeys is every key a resolved launch profile would set that a
// header-mode launch cares about: its env, its config_content's and
// config_defaults' env tables and their top-level keys (a claude
// config_content becomes a settings file whose env and apiKeyHelper outrank
// the process env). Sorted, no duplicates.
func launchProfileKeys(rp profiles.ResolvedProfile) []string {
	var keys []string
	for k := range rp.Env {
		keys = append(keys, k)
	}
	for _, cc := range []map[string]any{rp.ConfigContent, rp.ConfigDefaults} {
		for k, v := range cc {
			keys = append(keys, k)
			if k == "env" {
				if table, ok := v.(map[string]any); ok {
					for ek := range table {
						keys = append(keys, ek)
					}
				}
			}
		}
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

// slotProfileKeys is the keys of a slot profile's own env and
// config_content (launchProfileKeys' reading): the guard judges them before
// the register, ahead of the slot build's own refusal.
func slotProfileKeys(sp profiles.SlotProfile) []string {
	return launchProfileKeys(profiles.ResolvedProfile{Env: sp.Env, ConfigContent: sp.ConfigContent})
}

// launchProfileKeysFor is launchProfileKeys of the launch profile that
// matches agent and m, nil when none does (or profiles.toml does not load,
// which disables profiles for the launch anyway). The guard reads it before
// the confirm prompt, so a profile the user would decline still refuses:
// the safer reading.
func launchProfileKeysFor(agent string, m config.Model, cfg *config.Config, pp *precomputedProfiles) []string {
	rp, matched, err := resolveLaunchProfile(agent, m, cfg, pp)
	if err != nil || !matched {
		return nil
	}
	return launchProfileKeys(rp)
}

// slotRowsOf is the catalog rows of a slot profile's slot models, in
// FilledOrder (a model in several slots once): what the guard judges for a
// slot launch.
func slotRowsOf(rows []modelapi.Row, c profiles.SlotCheck) []modelapi.Row {
	byID := catalog.WireByID(rows)
	var out []modelapi.Row
	seen := map[string]bool{}
	for _, s := range c.FilledOrder() {
		id := c.Profile.Slots[s]
		if r, ok := byID[id]; ok && !seen[id] {
			seen[id] = true
			out = append(out, r)
		}
	}
	return out
}

// headerModeProfileErr is the appliers' backstop (#597, decision 7): a
// header-mode command whose launch profile would replace the merged header
// or displace the checked login is refused, naming the key, never a value.
func headerModeProfileErr(rp profiles.ResolvedProfile) error {
	if err := agents.HeaderModeProfileConflict(rp.Env, rp.ConfigContent); err != nil {
		return fmt.Errorf("profiles.toml: %w", err)
	}
	if err := agents.HeaderModeProfileConflict(nil, rp.ConfigDefaults); err != nil {
		return fmt.Errorf("profiles.toml config_defaults: %w", err)
	}
	return nil
}

// tuiSubscriptionGuard is the guard the TUI runs (tui.ProfileLayer.Guard),
// inside a command, before it registers a client session or starts a model.
// It closes over the session (ms), the session config and the loaded
// profiles: a single-model launch's matching launch profile, or a slot
// launch's own slot profile (tui.GuardLaunch.SlotProfile), is judged too
// (tui.GuardLaunchOf names the agent); a slot launch applies no launch
// profile. Its notes wait in the TUI's note queue for the terminal.
func tuiSubscriptionGuard(a *app, ms *modelSession, scfg *config.Config) tui.SubscriptionGuard {
	return func(ctx context.Context, rows []modelapi.Row) error {
		gl := tui.GuardLaunchOf(ctx)
		var keys []string
		switch {
		case gl.Slot && gl.SlotProfile != nil:
			keys = slotProfileKeys(*gl.SlotProfile)
		case !gl.Slot && len(rows) == 1 && gl.Agent != "":
			keys = launchProfileKeysFor(gl.Agent, catalog.ToModel(rows[0]), scfg, a.profileState())
		}
		w := gl.Notes
		if w == nil {
			w = osStderr
		}
		return subscriptionGuardTo(ctx, w, ms, rows, keys, a.hooks.login)
	}
}
