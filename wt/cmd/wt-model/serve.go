package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelserver"
	"github.com/ohanaverse/local-ai-setup/wt/internal/servestate"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tlsfile"
	"github.com/spf13/cobra"
)

// defaultIdleGrace is how long an unused model survives before auto-stop.
const defaultIdleGrace = 10 * time.Minute

// announceFormat is the one line serve prints on stderr once it is listening;
// %s is the bound address (ln.Addr(), so an ephemeral port reads as the port
// the kernel gave). It is the only machine-readable startup signal (S1).
const announceFormat = "wt-model: listening on %s (gateway /v1, control /control/v1)\n"

// announceLine is announceFormat for addr.
func announceLine(addr string) string { return fmt.Sprintf(announceFormat, addr) }

// depsFactory builds the server's dependencies from the host config and the
// LiteLLM upstream; main passes modelserver.DefaultDeps, tests a fake. reload re-reads the host config on each use (config.Load).
type depsFactory func(cfg *config.Config, reload func() (*config.Config, error), upstream *url.URL, upstreamKey string) modelserver.Deps

// loadServeConfig loads the host config and the LiteLLM base URL and key the
// gateway forwards to LiteLLM with, as Load's merge resolves them (the
// wt-model config home first, config.toml's legacy [litellm] table the
// read-only fallback).
func loadServeConfig() (*config.Config, *url.URL, string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, "", config.ConfigError(err)
	}
	// The pair is what serve forwards with: a home Load could not resolve
	// (a loose key file, an unparseable home) refuses the start here, the one
	// place it matters, with the load's own wording and hint.
	if perr := cfg.ProxyErr(); perr != nil {
		return nil, nil, "", config.ConfigError(perr)
	}
	base, key := cfg.LitellmBaseURL(), cfg.LitellmAPIKey()
	if base == "" || key == "" {
		return nil, nil, "", errors.New("the LiteLLM proxy is not configured: run 'wt-model litellm set --url URL --api-key KEY'")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		// A url with an '@' is never quoted (#640): the placeholder stands in.
		shown := strconv.Quote(base)
		if config.ProxyURLHasAt(base) {
			shown = config.ProxyURLHidden
		}
		return nil, nil, "", fmt.Errorf("the LiteLLM url %s is not a valid URL: run 'wt-model litellm set --url URL --api-key KEY'", shown)
	}
	return cfg, u, key, nil
}

// recordNoteFormat is the one stderr note when the runtime record cannot be
// written; serve goes on serving. %s is the write error.
const recordNoteFormat = "wt-model: note: could not write the runtime record (%s) — host commands cannot see this server's client sessions\n"

// serveCmd is `wt-model serve`. rt.Record is where it keeps its runtime
// record while it runs; h's ProxyEnv and ModelInfo are what its subscription
// hazard check reads.
func serveCmd(mk depsFactory, rt serveRuntime, h *host) *cobra.Command {
	var listen, tokenFile string
	idle := defaultIdleGrace
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the model server",
		Long: `Run the model server: the control API under /control/v1 starts and stops
wt's local models for agent sessions that run elsewhere, and the gateway under
/v1 forwards model requests to the LiteLLM proxy configured by
'wt-model litellm set --url URL --api-key KEY'.
Every request needs the shared bearer token, on every address.

Flags:
  --listen ADDR       address to listen on, host:port (required)
  --token-file PATH   file holding the shared bearer token, mode 0600 (required)
  --idle-grace DUR    how long an unused model survives before auto-stop
                      (default 10m0s, must be greater than zero)

Once listening it prints one line on stderr naming the bound address, then
reconciles the LiteLLM routes once, as 'wt-model litellm sync' does
(local routes through the proxy's model API when the pair is set): a
database that was wiped is repopulated the next time wt-model serve starts
(a proxy restart keeps its database rows and runs no sync). What that sync
changes or warns about is printed on stderr; a failure there is a warning
and serving goes on.
The subscription passthrough (the gateway's header mode: a request carrying
x-wt-model-token forwards the client's own claude.ai login, the owner's own
login only) is off unless the wt-model home's config.toml says

  [gateway]
  subscription_passthrough = true

a hand edit no command writes, read once at start (restart serve after a
change). When on, a line after the listening line says so, and a warning
follows when the address is not loopback and TLS is off, since header-mode
requests from other machines over plain http are refused. A value that is
not a boolean is off with one warning; a config.toml that does not parse
refuses the start. Before listening with the mode on, serve checks the LiteLLM
proxy (up to 5 s): an environment it cannot read (wt's own never stands
in), an ANTHROPIC_* credential or address variable in it (ANTHROPIC_API_KEY,
ANTHROPIC_AUTH_TOKEN, the workload-identity ones, ANTHROPIC_API_BASE,
ANTHROPIC_BASE_URL), client-header forwarding in config.yaml, a config.yaml it
cannot read, a wildcard routed to anthropic, or an anthropic deployment that
names a litellm credential or whose api_base or base_url is not exactly
https://api.anthropic.com keeps the mode off with one warning naming it; a
proxy that does not answer is one warning and the mode stays on (the route
sync keeps the Claude subscription routes that exist and adds none until it
can check). While the mode is on, serve repeats that check every minute: the
first hazard it finds turns the mode off for the rest of the run (the ping
stops advertising it, a header-mode request is a 403 subscription_off) with
one warning naming the hazard; it never turns back on without a restart.
TLS is off (plain http) unless the same table names a PEM certificate and
its key, both hand-edited, both or neither:

  [gateway]
  tls_cert = "~/.config/wt-model/tls/server.crt"
  tls_key  = "~/.config/wt-model/tls/server.key"

The key file must be mode 0600 or stricter, the certificate file must hold
no private key, and the two must be different files that match. Serve then
speaks TLS 1.2+ (HTTP/2 on), prints "TLS is on" after the listening line
(plus a warning when the certificate expires within 14 days or names
neither 127.0.0.1 nor localhost) and records the certificate's path in
serve.json. A certificate that is expired or not yet valid is not a refusal:
serve starts on plain http with one warning naming the date, the ping says
tls_off, remote https clients fail and the header mode is refused from
non-loopback peers until both files are replaced and serve restarted.
While it runs it keeps a runtime record, serve.json in the wt-model home
(pid, address, token file path; never the token), and removes it when it
stops cleanly.

Exit codes:
  0  stopped cleanly on SIGINT or SIGTERM
  1  any failure (a bad flag, an unreadable token file, an unconfigured
     LiteLLM proxy, a wt-model config.toml that does not parse, a
     tls_cert/tls_key that is half set, not a string, missing, unreadable,
     a loose key, a key in the certificate file, one file named twice or a
     pair that does not match or parse, an address that cannot be bound)`,
		Example: `  wt-model serve --listen 127.0.0.1:8787 --token-file ~/.config/wt-model/server.token`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listen == "" || tokenFile == "" {
				return errors.New("--listen and --token-file are required: wt-model serve --listen HOST:PORT --token-file PATH")
			}
			if idle <= 0 {
				return fmt.Errorf("--idle-grace must be greater than zero, got %s", idle)
			}
			token, err := modelserver.LoadToken(tokenFile)
			if err != nil {
				return err
			}
			cfg, upstream, key, err := loadServeConfig()
			if err != nil {
				return err
			}
			// A pair the legacy [litellm] fallback supplied prints the one
			// fallback note before anything listens (ruling 7): the fallback
			// is read-only now, and the note names the writer. The source is
			// the one from the load above (cfg.ProxySource) — the pair being
			// forwarded and the pair judged for the note are the same
			// resolution, never two that could disagree (fix round 2,
			// Finding 4).
			if cfg.ProxySource() == config.ProxySourceLegacy {
				fmt.Fprintln(cmd.ErrOrStderr(), config.ProxyFallbackNote())
			}
			// The host switch is read once, here, before listening: a home
			// that does not parse refuses the start (worded without values),
			// a wrong-typed key is off with a warning printed after the
			// announce line. A change needs a serve restart.
			gw, gwWarns, err := config.GatewaySettings()
			if err != nil {
				// A TLS key serve cannot use is its own one line
				// (decision 13: fail closed); anything else is the
				// home that does not parse.
				var ke *config.GatewayKeyError
				if errors.As(err, &ke) {
					return ke
				}
				return config.ConfigError(&config.ProxyHomeError{Err: err})
			}
			// With the mode on, the proxy is checked for what would make a
			// keyless anthropic/ row unsafe (decision 5), synchronously and
			// bounded: a hazard keeps the mode off (the ping says so), a
			// proxy that cannot be read keeps it on (the sync gates the
			// routes). Its one warning line follows the announce line.
			gateLine := ""
			if gw.SubscriptionPassthrough {
				gw.SubscriptionPassthrough, gateLine = subscriptionGate(cmd.Context(), litellm.PairedConfigPath(), h.ProxyEnv(), h.ModelInfo, subscriptionGateBound)
			}
			// TLS is loaded here, before listening and before serve.json:
			// a configuration error refuses the start; an expired or
			// not-yet-valid certificate serves plain http with one
			// warning after the announce line.
			now := time.Now()
			st, err := loadServeTLS(gw, now)
			if err != nil {
				return err
			}
			// The proxy's transport errors (and, with TLS, the handshake
			// errors) go to serve's stderr; it logs no header or body
			// (the gateway's rule).
			errLog := log.New(cmd.ErrOrStderr(), "wt-model: ", 0)
			srv, err := modelserver.NewServer(serverOptions(listen, token, idle, upstream, key, gw, st, errLog),
				mk(cfg, config.Load, upstream, key))
			if err != nil {
				return err
			}
			recordCert := ""
			if st.Config != nil {
				recordCert = st.Cert
			}
			// The record names the token file by an absolute path, so a host
			// command run from another directory finds it; never the token.
			abs, aerr := filepath.Abs(tokenFile)
			if aerr != nil {
				abs = tokenFile
			}
			// Remove deletes the record only while it names this pid: one a
			// later serve wrote over it stays.
			defer func() { _ = rt.Record.Remove(os.Getpid()) }()
			// The start reconcile (rt.StartSync) runs once the server is
			// listening, off the serving path: it waits for the proxy (up to
			// 90 s at login) and lists its database, and a session must be
			// able to reach the server meanwhile. serve waits for it before
			// returning, so a stop never cuts a config.yaml write short; every
			// wait in it ends with the serve's context, and it restarts no
			// proxy itself (the restart it owes is the tracked bounce the
			// shutdown's route-settle step waits for, like a job's).
			var startSync sync.WaitGroup
			defer startSync.Wait()
			// While the header mode is on, the start gate's check repeats
			// (#697), so a hazard that comes after the start turns the mode
			// off at once for the gateway, the ping and the catalog.
			var watch sync.WaitGroup
			defer watch.Wait()
			// The watcher ends with serve's context, and also when Serve
			// returns on its own (a listener error leaves cmd.Context()
			// live): watchCtx is cancelled first (defers run last-in
			// first-out), so watch.Wait cannot hang.
			watchCtx, stopWatch := context.WithCancel(cmd.Context())
			defer stopWatch()
			return srv.Serve(cmd.Context(), func(addr string) {
				// The record lands before the announce line, so whoever saw
				// the line can read the record.
				if err := rt.Record.Write(servestate.Record{
					PID: os.Getpid(), Listen: addr, TokenFile: abs, Started: time.Now().UTC(),
					TLSCert: recordCert,
				}); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), recordNoteFormat, err)
				}
				fmt.Fprint(cmd.ErrOrStderr(), announceLine(addr))
				// The TLS state and the mode follow the announce line,
				// which stays first and unchanged (S1).
				for _, l := range tlsLines(st.Cert, st.Leaf, now) {
					fmt.Fprintln(cmd.ErrOrStderr(), l)
				}
				for _, w := range gwWarns {
					fmt.Fprintln(cmd.ErrOrStderr(), "wt-model: warning: "+w)
				}
				if gateLine != "" {
					fmt.Fprintln(cmd.ErrOrStderr(), gateLine)
				}
				for _, l := range subscriptionLines(addr, gw.SubscriptionPassthrough, st.Config != nil, st.Off) {
					fmt.Fprintln(cmd.ErrOrStderr(), l)
				}
				if gw.SubscriptionPassthrough {
					watch.Add(1)
					go func() {
						defer watch.Done()
						watchSubscription(watchCtx, subscriptionRecheckEvery,
							func(ctx context.Context) ([]string, error) {
								hazards, _, err := litellm.SubscriptionHazards(ctx, litellm.PairedConfigPath(), h.ProxyEnv(), h.ModelInfo)
								return hazards, err
							},
							srv.DisableSubscriptionPassthrough, cmd.ErrOrStderr())
					}()
				}
				// After the announce line, so it stays the first line a
				// script reads (S1).
				if rt.StartSync != nil {
					startSync.Add(1)
					go func() {
						defer startSync.Done()
						rt.StartSync(cmd.Context(), cfg, cmd.ErrOrStderr())
					}()
				}
			})
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "", "address to listen on, host:port")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "file holding the shared bearer token (mode 0600); required on every address")
	cmd.Flags().DurationVar(&idle, "idle-grace", defaultIdleGrace, "how long an unused model survives before auto-stop (must be > 0)")
	return cmd
}

// serveStartSync is the route sync `wt-model serve` runs once as it starts
// (#598): a proxy restarted, or one whose database was wiped, has lost every
// database route, and nothing else would put them back until the next start
// or stop. It is startSync with the real probe, readiness wait and restart.
func serveStartSync(probe litellm.Probe) func(ctx context.Context, cfg *config.Config, errOut io.Writer) {
	return startSync{
		probe:        probe,
		waitReady:    litellm.WaitReady,
		bounce:       lifecycle.BounceProxyTo,
		readyTimeout: startSyncReadyTimeout,
		listTimeout:  startSyncListTimeout,
	}.run
}

// startSyncReadyTimeout bounds the start sync's wait for the proxy to answer:
// at login both LaunchAgents start together, and the proxy (Python, then its
// database) answers well after serve listens. startSyncListTimeout bounds
// the listing that proves its database answers too.
const (
	startSyncReadyTimeout = 90 * time.Second
	startSyncListTimeout  = 15 * time.Second
)

// startSync is the start reconcile's parts, each a per-value seam: probe is
// the local inventory, waitReady the wait for the proxy's liveness endpoint
// (litellm.WaitReady), bounce the restart owed when the sync changed
// config.yaml (lifecycle.BounceProxyTo: asynchronous, one restart at a time,
// waited for by WaitPendingRoutes at exit).
type startSync struct {
	probe                     litellm.Probe
	waitReady                 func(ctx context.Context, baseURL string, timeout time.Duration) error
	bounce                    func(ctx context.Context, out io.Writer, cfg *config.Config)
	readyTimeout, listTimeout time.Duration
}

// startSyncSkipped is the one warning of a start sync that did not run
// because it could not see the database; %s says why.
const startSyncSkipped = "wt-model: warning: LiteLLM routes not synced at start: %s; nothing was changed (run 'wt-model litellm sync' once the proxy answers)\n"

// run is the start reconcile: `wt-model litellm sync` (litellm.SyncInputs
// over probe, then litellm.Sync with the proxy pair's model API, the same
// Options), reporting on errOut.
//
// It runs only when it can see the database: it first waits for the proxy
// to answer (readyTimeout) and then lists its database (listTimeout). A
// proxy that is still starting, or a database that cannot be listed, skips
// the whole sync with one warning and writes nothing: a sync that cannot see
// the database would put every local route back in config.yaml and restart a
// proxy that is still coming up.
//
// The write never restarts the proxy itself (NoRestart): a sync that changed
// config.yaml (the first-time setting, the migration, a cloud route) hands
// the restart to bounce, so it runs one at a time with the route jobs'
// restarts and the serve's stop never waits on it here. Every wait honors
// ctx. A failure is one warning line and never stops serving; no config.yaml
// at all (LiteLLM not set up here) is silent.
func (s startSync) run(ctx context.Context, cfg *config.Config, errOut io.Writer) {
	if _, err := os.Stat(litellm.DefaultPath()); errors.Is(err, os.ErrNotExist) {
		return
	}
	admin := adminFromPair(cfg.LitellmBaseURL(), cfg.LitellmAPIKey())
	if admin == nil {
		fmt.Fprintf(errOut, startSyncSkipped, "the LiteLLM proxy pair is not set")
		return
	}
	if err := s.waitReady(ctx, admin.BaseURL, s.readyTimeout); err != nil {
		fmt.Fprintf(errOut, startSyncSkipped, fmt.Sprintf("the proxy did not answer within %s (%v)", s.readyTimeout, err))
		return
	}
	lctx, cancel := context.WithTimeout(ctx, s.listTimeout)
	_, lerr := admin.List(lctx)
	cancel()
	if lerr != nil {
		fmt.Fprintf(errOut, startSyncSkipped, "its database routes could not be listed ("+strings.Join(strings.Fields(lerr.Error()), " ")+")")
		return
	}
	desired, o, probeWarns := litellm.SyncInputs(cfg, s.probe)
	o.Admin, o.Ctx, o.NoRestart = admin, ctx, true
	res, err := litellm.Sync(cfg, desired, o)
	if errors.Is(err, litellm.ErrMissing) {
		return
	}
	if err != nil {
		fmt.Fprintf(errOut, "wt-model: warning: LiteLLM routes not synced at start: %v\n", err)
		return
	}
	for _, oc := range res.Outcomes {
		if oc.Err != nil {
			fmt.Fprintf(errOut, "wt-model: warning: start route sync: %s: %v\n", oc.ID, oc.Err)
			continue
		}
		fmt.Fprintf(errOut, "wt-model: start route sync: %s: %s\n", oc.ID, oc.Action)
	}
	for _, w := range append(res.Warnings, probeWarns...) {
		fmt.Fprintf(errOut, "wt-model: warning: start route sync: %s\n", w)
	}
	if res.Changed {
		s.bounce(ctx, errOut, cfg)
	}
}

// serveTLS is what serve does with [gateway] tls_cert/tls_key.
type serveTLS struct {
	// Config is the TLS config serve speaks; nil is plain http.
	Config *tls.Config
	// Leaf is the certificate's leaf, set whenever one was loaded (also
	// when its dates turned TLS off), for tlsLines.
	Leaf *x509.Certificate
	// Cert is tls_cert's absolute path ("" when TLS is not configured).
	Cert string
	// Off is the date reason TLS is off although configured (the ping's
	// tls_off); empty otherwise.
	Off string
}

// tlsRefusalPrefix leads every refusal of a TLS pair serve cannot use: one
// line, wt-model: [gateway] tls_cert/tls_key: <fixed reason> (<path>).
const tlsRefusalPrefix = "[gateway] tls_cert/tls_key: "

// loadServeTLS loads the pair gw names at now (decision 13). No pair is
// plain http. A configuration error (a missing or unreadable file, a loose
// key, a non-regular file, a key in the certificate file, one file named
// twice, a mismatched or unparseable pair) is the one refusal line; an
// expired or not-yet-valid certificate (tlsfile checks the dates last, so
// every configuration check passed) is plain http with Off set and the leaf
// kept for the warning. No message holds key or certificate bytes.
func loadServeTLS(gw config.GatewayConf, now time.Time) (serveTLS, error) {
	if gw.TLSCert == "" {
		return serveTLS{}, nil
	}
	cert := gw.TLSCert
	if a, err := filepath.Abs(cert); err == nil {
		cert = a
	}
	pair, leaf, err := tlsfile.Load(gw.TLSCert, gw.TLSKey, now)
	if err != nil {
		var te *tlsfile.Error
		if errors.As(err, &te) && te.ClockOnly() {
			return serveTLS{Leaf: leaf, Cert: cert, Off: te.DateReason()}, nil
		}
		return serveTLS{}, errors.New(tlsRefusalPrefix + err.Error())
	}
	return serveTLS{Config: tlsfile.Config(pair), Leaf: leaf, Cert: cert}, nil
}

// serverOptions is the modelserver.Options serve runs with: the flags, the
// proxy pair, the home's [gateway] switch and the TLS state.
func serverOptions(listen, token string, idle time.Duration, upstream *url.URL, key string, gw config.GatewayConf, st serveTLS, errLog *log.Logger) modelserver.Options {
	return modelserver.Options{
		Listen: listen, Token: token, IdleGrace: idle,
		Upstream: upstream, UpstreamKey: key,
		SubscriptionPassthrough: gw.SubscriptionPassthrough,
		ErrorLog:                errLog,
		TLS:                     st.Config,
		TLSOff:                  st.Off,
	}
}

// The TLS report's lines (decision 13): the bare info line while TLS is on,
// the two warnings that may follow it, and the one warning when the
// certificate's dates turned TLS off.
const (
	tlsOnFormat         = "wt-model: TLS is on (tls_cert %s, TLS 1.2+; clients that do not trust it need ca_file or a CA the system trusts)"
	tlsExpiringFormat   = "wt-model: warning: the certificate %s expires on %s (%s): renew it (replace both files, then restart serve)"
	tlsNoLoopbackFormat = "wt-model: warning: the certificate %s names neither 127.0.0.1 nor localhost; local sessions dial loopback and will refuse it"
	tlsOffFormat        = "wt-model: warning: TLS is off: the certificate %s %s; serving plain http. Remote https clients fail and the subscription header mode is refused from non-loopback peers until it is renewed and serve restarted"
)

// tlsExpiringWithin is how close to its end a certificate earns the
// expiring-soon warning.
const tlsExpiringWithin = 14 * 24 * time.Hour

// tlsLines is serve's whole TLS report, the lines printed after the announce
// line: nothing without a certificate; for an expired or not-yet-valid one
// only the plain-http warning; else the TLS-on line, then a warning when it
// expires within 14 days, then one when it names neither 127.0.0.1 nor
// localhost (local sessions dial loopback).
func tlsLines(certPath string, leaf *x509.Certificate, now time.Time) []string {
	if certPath == "" || leaf == nil {
		return nil
	}
	day := func(t time.Time) string { return t.UTC().Format("2006-01-02") }
	switch {
	case now.Before(leaf.NotBefore):
		return []string{fmt.Sprintf(tlsOffFormat, certPath, "is not valid until "+day(leaf.NotBefore))}
	case now.After(leaf.NotAfter):
		return []string{fmt.Sprintf(tlsOffFormat, certPath, "expired on "+day(leaf.NotAfter))}
	}
	lines := []string{fmt.Sprintf(tlsOnFormat, certPath)}
	if left := leaf.NotAfter.Sub(now); left <= tlsExpiringWithin {
		lines = append(lines, fmt.Sprintf(tlsExpiringFormat, certPath, day(leaf.NotAfter), daysLeft(left)))
	}
	if !namesLoopback(leaf) {
		lines = append(lines, fmt.Sprintf(tlsNoLoopbackFormat, certPath))
	}
	return lines
}

// daysLeft words the time a certificate has left, in whole days.
func daysLeft(d time.Duration) string {
	switch n := int(d / (24 * time.Hour)); n {
	case 0:
		return "within a day"
	case 1:
		return "in 1 day"
	default:
		return fmt.Sprintf("in %d days", n)
	}
}

// namesLoopback reports whether the certificate names 127.0.0.1 or
// localhost, the names a local session dials.
func namesLoopback(leaf *x509.Certificate) bool {
	for _, ip := range leaf.IPAddresses {
		if ip.Equal(net.IPv4(127, 0, 0, 1)) {
			return true
		}
	}
	for _, d := range leaf.DNSNames {
		if strings.EqualFold(d, "localhost") {
			return true
		}
	}
	return false
}

// subscriptionInfo is the bare informational line (style guide M4) serve
// prints after the announce line while the header mode is on; %s is the
// optional tail (the TLS-off reason) before the closing parenthesis.
const subscriptionInfo = "wt-model: subscription passthrough is on (x-wt-model-token; refused from non-loopback peers over plain http; owner's own login only%s)"

// subscriptionWarn is the one warning when the mode is on, the listen
// address is not loopback and TLS is off: remote header-mode requests are
// refused, so the operator learns how to make them work. It names the
// config.toml serve actually reads (#700).
func subscriptionWarn() string {
	return "wt-model: warning: remote subscription requests need TLS (set tls_cert and tls_key under [gateway] in " + config.ProxyHomeFile() + ") or a tunnel arriving from loopback; other remote traffic is unaffected"
}

// subscriptionLines is serve's whole report of the header mode, the lines
// printed after the announce line (decision 2 of the phase 3 plan): nothing
// when off; else the info line, plus subscriptionWarn when listenAddr is not
// loopback and TLS is off. tlsOff, when set, is the fixed reason TLS is off
// although configured (the certificate's dates): the info line then ends
// with it and no second warning is printed (the TLS report has its own).
func subscriptionLines(listenAddr string, on, tlsOn bool, tlsOff string) []string {
	if !on {
		return nil
	}
	if tlsOff != "" {
		return []string{fmt.Sprintf(subscriptionInfo, "; TLS is off: "+tlsOffPhrase(tlsOff)+", so header mode is refused from every non-loopback peer")}
	}
	lines := []string{fmt.Sprintf(subscriptionInfo, "")}
	if !tlsOn && !loopbackListen(listenAddr) {
		lines = append(lines, subscriptionWarn())
	}
	return lines
}

// tlsOffPhrase words tlsfile's date reason for the info line (decision 2):
// "certificate expired <date>" reads "the certificate expired on <date>",
// "certificate not valid until <date>" reads "the certificate is not valid
// until <date>"; any other reason is prefixed with "the ".
func tlsOffPhrase(reason string) string {
	if d, ok := strings.CutPrefix(reason, "certificate expired "); ok {
		return "the certificate expired on " + d
	}
	if d, ok := strings.CutPrefix(reason, "certificate not valid until "); ok {
		return "the certificate is not valid until " + d
	}
	return "the " + reason
}

// loopbackListen reports whether a listen address binds loopback only:
// localhost or a loopback IP. An unspecified host (0.0.0.0, ::, empty) and
// any other address are reachable from other machines.
func loopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}

// subscriptionGateBound bounds serve's start-time hazard check: at login the
// proxy may still be starting, and serve must not wait on it to listen.
const subscriptionGateBound = 5 * time.Second

// subscriptionRecheckEvery (a variable only so tests can shorten it) is how often serve re-runs the hazard check while
// the header mode is on (#697): the longest a hazard that arrives after the
// start leaves the mode on. The check reads config.yaml, the proxy's
// environment and its /model/info, so a minute is cheap.
var subscriptionRecheckEvery = time.Minute

// watchSubscription re-runs the start gate's check every `every` until ctx
// ends. The first tick that finds a hazard calls disable (the server's switch,
// which the gateway, the ping and the catalog all read) and prints one line
// naming it and the way back, then the watcher returns: the mode stays off for
// the run, because a check that clears later is no proof the hazard did not
// come and go. A check that finds no hazard, or cannot be made (the proxy
// restarting, slow), changes nothing and says nothing, as the start gate
// keeps the mode on for an unreadable proxy and leaves the routes to the sync.
func watchSubscription(ctx context.Context, every time.Duration, check func(context.Context) ([]string, error), disable func() bool, errOut io.Writer) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		cctx, cancel := context.WithTimeout(ctx, subscriptionGateBound)
		hazards, _ := check(cctx)
		cancel()
		if len(hazards) == 0 {
			continue
		}
		if disable() {
			fmt.Fprintln(errOut, "wt-model: warning: subscription passthrough turned off while serving: "+strings.Join(hazards, "; ")+" (fix it, then restart wt-model serve)")
		}
		return
	}
}

// subscriptionGate is serve's start-time check of the proxy while the header
// mode is switched on (decision 5): litellm.SubscriptionHazards over the
// config.yaml at cfgPath (an empty path is the "config.yaml not identified"
// hazard: the mode stays off), the proxy's environment and its
// deployments, bounded by bound. A hazard keeps the mode off, with one warning
// naming it (never a value); a proxy that cannot be read within the bound
// keeps the mode on with one warning, because the route sync leaves every
// subscription row unrouted until it can check. on reports the mode serve
// runs with; line is the one warning, "" when there is none.
func subscriptionGate(ctx context.Context, cfgPath string, env litellm.ProxyEnv, info litellm.ModelInfoFunc, bound time.Duration) (on bool, line string) {
	ctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	hazards, _, err := litellm.SubscriptionHazards(ctx, cfgPath, env, info)
	switch {
	case len(hazards) > 0:
		return false, "wt-model: warning: subscription passthrough is kept off: " + strings.Join(hazards, "; ") + " (fix it, then restart wt-model serve)"
	case err != nil:
		return true, "wt-model: warning: subscription passthrough is on, but the proxy could not be checked for hazards (" + oneLineText(err.Error()) + "); the route sync keeps the Claude subscription routes that exist and adds none until it can check"
	}
	return true, ""
}
