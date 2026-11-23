package agents

import (
	"net/url"
	"strings"
)

// noProxyVars are the two spellings of the proxy bypass list: Node's undici
// and most tools read both, and some read only one.
var noProxyVars = []string{"NO_PROXY", "no_proxy"}

// noProxyEnv is the proxy leak guard of a claude header-mode launch (#597):
// over a plain http gateway (header mode permits that only to loopback) the
// request carries the claude.ai login and the wt token in clear, and an
// inherited HTTP_PROXY/HTTPS_PROXY/ALL_PROXY could route it through a proxy
// (whether Claude Code skips a proxy for loopback by itself is unverified).
// So the gateway's host is added to both NO_PROXY and no_proxy: appended to
// an inherited value with a comma (never replacing it), and not added again
// when already listed. A spelling that is absent starts from the other's
// inherited value (undici, curl and Python read the lowercase one first, Go
// the uppercase one), so the user's bypass list is never hidden; when both
// are set and differ, each keeps its own value. An inherited "*" stays (the
// append is harmless). With neither set, both hold just the host. Every other launch (token mode,
// an https origin, native with no origin, an agent other than claude) gets
// env back unchanged.
//
// An IPv6 literal goes in both spellings, ::1 and [::1]: undici and
// proxy-from-env compare against the URL's bracketed host while Go and curl
// match the bare address, and which one Claude Code's client uses is not
// verified, so both are listed.
func noProxyEnv(agent, origin string, header bool, env []string) []string {
	if agent != "claude" || !header || !strings.HasPrefix(strings.ToLower(origin), "http://") {
		return env
	}
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" {
		return env
	}
	host := strings.ToLower(u.Hostname())
	hosts := []string{host}
	if strings.Contains(host, ":") {
		hosts = append(hosts, "["+host+"]")
	}
	// Read both spellings before changing either: an absent one starts from
	// the other's inherited value (tools read one spelling first, so the
	// user's own bypass list must stay visible under both); two that are set
	// keep their own values (never merged across).
	inherited := map[string]string{}
	present := map[string]bool{}
	for _, name := range noProxyVars {
		inherited[name], present[name] = lookupLast(env, name)
	}
	for i, name := range noProxyVars {
		cur, set := inherited[name], present[name]
		base := cur
		if other := noProxyVars[1-i]; !set && present[other] {
			base = inherited[other]
		}
		listed := map[string]bool{}
		for _, e := range strings.Split(base, ",") {
			listed[strings.ToLower(strings.TrimSpace(e))] = true
		}
		val := base
		for _, h := range hosts {
			if listed[h] {
				continue
			}
			if strings.TrimSpace(val) == "" {
				val = h
			} else {
				val += "," + h
			}
		}
		if set && val == cur {
			continue
		}
		env = append(filterEnv(env, []string{name}), name+"="+val)
	}
	return env
}

// lookupLast is the value of the last entry naming key in env and whether
// one exists (the one a child process sees).
func lookupLast(env []string, key string) (string, bool) {
	v, found := "", false
	for _, e := range env {
		if k, val, ok := strings.Cut(e, "="); ok && k == key {
			v, found = val, true
		}
	}
	return v, found
}
