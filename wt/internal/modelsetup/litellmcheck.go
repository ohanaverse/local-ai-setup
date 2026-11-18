package modelsetup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
)

// CheckLiteLLM probes the LiteLLM proxy named by pair and the files that start
// it. Every Detail is short, lower-case ASCII and free of secrets: the API key
// is passed only as the bearer of one Net.Get and no error text from the
// network is copied into a Detail (it could echo a header). The plist and
// config.yaml paths come from env.LiteLLM.
func CheckLiteLLM(ctx context.Context, env Env, pair config.ProxyPair) LiteLLMState {
	var st LiteLLMState
	if pair.URL == "" || pair.APIKey == "" {
		why := Check{Detail: "proxy url or key not set"}
		st.Reachable, st.Database, st.KeyAccepted, st.RoutesInSync = why, why, why, why
		st.RestartOwed, st.PortMatches, st.PlistMode = why, why, why
		return st
	}
	st.Checked = true
	base := strings.TrimRight(pair.URL, "/")

	// Network checks.
	skip := Check{Detail: "proxy not reachable"}
	status, _, err := env.Net.Get(ctx, base+"/health/liveliness", "")
	switch {
	case err != nil:
		st.Reachable = Check{CheckFail, probeFailure(ctx)}
	case status != 200:
		st.Reachable = Check{CheckFail, "status " + strconv.Itoa(status)}
	default:
		st.Reachable = Check{CheckOK, "answers"}
	}
	if st.Reachable.Status == CheckOK {
		st.Database = checkDatabase(ctx, env, base)
		var ids []string
		st.KeyAccepted, ids = checkKey(ctx, env, base, pair.APIKey)
		st.RoutesInSync = checkRoutes(ctx, env, st.KeyAccepted.Status == CheckOK, ids)
	} else {
		st.Database, st.KeyAccepted, st.RoutesInSync = skip, skip, skip
	}

	// File and process checks, which need no answer from the proxy.
	st.RestartOwed = checkRestartOwed(ctx, env)
	st.PortMatches, st.PlistMode = checkPlist(env, base)
	return st
}

// probeFailure names why a probe got no answer without quoting the error.
func probeFailure(ctx context.Context) string {
	if ctx.Err() != nil {
		return "check cancelled"
	}
	return "no answer"
}

func checkDatabase(ctx context.Context, env Env, base string) Check {
	status, body, err := env.Net.Get(ctx, base+"/health/readiness", "")
	if err != nil || status != 200 {
		return Check{CheckWarn, "readiness not answered"}
	}
	var r struct {
		DB string `json:"db"`
	}
	if json.Unmarshal(body, &r) != nil || r.DB != "connected" {
		return Check{CheckWarn, "database not connected"}
	}
	return Check{CheckOK, "connected"}
}

func checkKey(ctx context.Context, env Env, base, key string) (Check, []string) {
	status, body, err := env.Net.Get(ctx, base+"/v1/models", key)
	switch {
	case err != nil:
		return Check{CheckFail, "model list " + probeFailure(ctx)}, nil
	case status == 401 || status == 403:
		return Check{CheckFail, "key rejected by the proxy"}, nil
	case status != 200:
		return Check{CheckFail, "status " + strconv.Itoa(status)}, nil
	}
	var r struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &r) != nil {
		return Check{CheckWarn, "model list unreadable"}, nil
	}
	ids := make([]string, 0, len(r.Data))
	for _, d := range r.Data {
		ids = append(ids, d.ID)
	}
	return Check{CheckOK, "accepted"}, ids
}

func checkRoutes(ctx context.Context, env Env, listed bool, have []string) Check {
	if env.DesiredRoutes == nil {
		return Check{Detail: "desired routes not available"}
	}
	if !listed {
		return Check{Detail: "model list not read"}
	}
	want, err := env.DesiredRoutes(ctx)
	if err != nil {
		return Check{Detail: "desired routes not readable"}
	}
	missing, extra := diffSets(want, have)
	if len(missing) == 0 && len(extra) == 0 {
		return Check{CheckOK, "in sync"}
	}
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, "missing "+nameList(missing))
	}
	if len(extra) > 0 {
		parts = append(parts, "extra "+nameList(extra))
	}
	return Check{CheckWarn, strings.Join(parts, "; ")}
}

// diffSets returns the sorted names in want but not have, and in have but not want.
func diffSets(want, have []string) (missing, extra []string) {
	w, h := map[string]bool{}, map[string]bool{}
	for _, n := range want {
		w[n] = true
	}
	for _, n := range have {
		h[n] = true
	}
	for n := range w {
		if !h[n] {
			missing = append(missing, n)
		}
	}
	for n := range h {
		if !w[n] {
			extra = append(extra, n)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

// nameList joins up to three names and counts the rest.
func nameList(names []string) string {
	if len(names) <= 3 {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:3], ", "), len(names)-3)
}

func checkRestartOwed(ctx context.Context, env Env) Check {
	job, err := env.Launchd.Print(ctx, litellm.ProxyLabel)
	if err != nil || !job.Running || job.PID <= 0 {
		return Check{Detail: "proxy not running under launchd"}
	}
	started, err := env.Net.ProcessStart(ctx, job.PID)
	if err != nil {
		return Check{Detail: "proxy start time unknown"}
	}
	info, err := env.FS.Stat(env.LiteLLM.ConfigPath())
	if err != nil {
		return Check{Detail: "config.yaml not readable"}
	}
	if info.ModTime().After(started) {
		return Check{CheckWarn, "config.yaml is newer than the proxy; restart it"}
	}
	return Check{CheckOK, "proxy has read config.yaml"}
}

func checkPlist(env Env, base string) (port, mode Check) {
	info, err := env.LiteLLM.ReadPlist(env.LiteLLM.PlistPath())
	if err != nil {
		unread := Check{Detail: "proxy plist not readable"}
		return unread, unread
	}
	switch want := urlPort(base); {
	case info.Port == 0:
		port = Check{CheckWarn, "plist has no --port"}
	case want == 0:
		port = Check{Detail: "proxy url has no port"}
	case info.Port != want:
		port = Check{CheckWarn, fmt.Sprintf("plist port %d, url port %d", info.Port, want)}
	default:
		port = Check{CheckOK, "port " + strconv.Itoa(want)}
	}
	if info.Mode != 0o600 {
		mode = Check{CheckWarn, fmt.Sprintf("plist mode %04o, want 0600", info.Mode)}
	} else {
		mode = Check{CheckOK, "mode 0600"}
	}
	return port, mode
}

// urlPort is the port of a URL, with the scheme's default; 0 when unknown.
func urlPort(raw string) int {
	u, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	if p, err := strconv.Atoi(u.Port()); err == nil {
		return p
	}
	switch u.Scheme {
	case "http":
		return 80
	case "https":
		return 443
	}
	return 0
}
