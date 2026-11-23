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
// an inherited value with a comma (never replacing it), created when absent,
// and not added again when already listed. Every other launch (token mode,
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
	for _, name := range noProxyVars {
		cur, set := lookupLast(env, name)
		listed := map[string]bool{}
		for _, e := range strings.Split(cur, ",") {
			listed[strings.ToLower(strings.TrimSpace(e))] = true
		}
		val := cur
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
