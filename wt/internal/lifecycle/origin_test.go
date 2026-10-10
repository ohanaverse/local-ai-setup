package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// servedAt is the network of a test about the address wt reaches a provider
// at. It records the origin of every request, forwards the ones for a host
// in to (a fake server on a free port) and refuses the rest, as a port with
// no listener does. So a test can stand a server on mtplx's default port and
// see which port wt asks, without binding that port or dialling whatever
// holds it on the machine the tests run on.
type servedAt struct {
	mu      sync.Mutex
	origins []string
	to      map[string]string // requested host:port -> where the fake listens
}

func (s *servedAt) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.origins = append(s.origins, r.URL.Scheme+"://"+r.URL.Host)
	addr, ok := s.to[r.URL.Host]
	s.mu.Unlock()
	if !ok {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	}
	fwd := r.Clone(r.Context())
	fwd.URL.Host = addr
	return http.DefaultTransport.RoundTrip(fwd)
}

// asked is every distinct origin requested so far.
func (s *servedAt) asked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(s.origins)
	slices.Sort(out)
	return slices.Compact(out)
}

// on makes e's HTTP go through s.
func (s *servedAt) on(e *env) {
	e.probeClient = &http.Client{Timeout: 5 * time.Second, Transport: s}
	e.chatClient = &http.Client{Transport: s}
}

// mtplxServeHost is where `wt start` serves mtplx when the registry names no
// port.
var mtplxServeHost = "127.0.0.1:" + strconv.Itoa(config.MtplxPort)

// TestMtplxStartAndItsCheckAskOnePortForABaseURLWithoutOne is #348 end to
// end. The registry's mtplx base_url names no port. The start spawns `mtplx
// serve` on wt's default port and waits for it there, so it succeeds; the
// check that follows the start (SettleStart's probe) must then ask that same
// port. It asked the url as written, port 80, where nothing listens: every
// `wt start mtplx/X` ended in "is not running: ... mtplx no longer answers at
// http://127.0.0.1", exit 1, with the model serving and its route removed.
// The occupant check before the start read the same wrong port.
func TestMtplxStartAndItsCheckAskOnePortForABaseURLWithoutOne(t *testing.T) {
	e, _, _, argvFile := mtplxEnv(t, "serve", 0)
	listen := freeAddr(t)
	t.Setenv("LIFECYCLE_HELPER_LISTEN", listen)
	network := &servedAt{to: map[string]string{mtplxServeHost: listen}}
	network.on(e)
	e.inventory = func(*config.Config) localmodels.Snapshot { return localmodels.Snapshot{} }
	cfg := provCfg("mtplx", "http://127.0.0.1/v1")
	target := Target{ProviderID: "mtplx", ModelName: "Org/Model", ModelID: "mtplx/Org--Model"}

	if err := start(context.Background(), e, cfg, target, Options{}); err != nil {
		t.Fatalf("start: %v", err)
	}
	argv, _ := os.ReadFile(argvFile)
	if want := fmt.Sprintf("--port %d --host 127.0.0.1", config.MtplxPort); !strings.Contains(string(argv), want) {
		t.Fatalf("mtplx was spawned with %q, want %q", argv, want)
	}
	if why := e.startedGone(context.Background(), cfg, target); why != "" {
		t.Errorf("the check after the start = %q, want the model found where it was started", why)
	}
	if got, want := network.asked(), []string{"http://" + mtplxServeHost}; !slices.Equal(got, want) {
		t.Errorf("wt asked %v, want only %v: the start, its occupant check and the check after it are one server's", got, want)
	}
}

// TestOccupantOfAnMtplxWithoutAPortIsSeen verifies the question a start asks
// before it spawns anything — what is this server serving — goes to the port
// wt serves mtplx on when the registry names none (#348). Asked of port 80 it
// was answered by a refused connection, which reads as "nothing is serving":
// a model already on the port was never offered for replacement, and the
// start went on to fail on a busy port.
func TestOccupantOfAnMtplxWithoutAPortIsSeen(t *testing.T) {
	srv, err := url.Parse(openaiSrv(t, []string{"Y/Q27"}))
	if err != nil {
		t.Fatal(err)
	}
	e := testEnv()
	network := &servedAt{to: map[string]string{mtplxServeHost: srv.Host}}
	network.on(e)
	ids, known := e.liveServed(context.Background(), provCfg("mtplx", "http://127.0.0.1/v1"), "mtplx")
	if !known || !slices.Equal(ids, []string{"Y/Q27"}) {
		t.Errorf("liveServed = %v, known %v; want [Y/Q27], known", ids, known)
	}
}

// TestStartProbeAndRouteAgreeOnAProvidersAddress is the contract #348 broke,
// for every local family and for base_urls with and without a port: the
// origin a start waits on, the origin wt probes (the inventory, the occupant
// check, the check after a start), the api_base of the model's LiteLLM route
// and the origin of a direct route are one address. Each is read from where
// production reads it: the start's from the backend (mtplx's own resolution;
// for omlx and ollama the first address their start asks, against a network
// where nothing listens), the others from localmodels.FamilyOrigin,
// litellm.BuildEntry and config.ResolveRoute. wt has no start for
// mlx_lm_server, so it has the other three.
//
// What the address is: the url as written (BaseOrigin), except an mtplx url
// on this machine with no port, which is the port wt serves mtplx on. An
// mtplx url with no port that is https or names a host the server wt starts
// does not answer on is not here: it has no start side, a start refuses it
// (TestMtplxStartRefusesABaseURLWithNoPortItCannotServe).
func TestStartProbeAndRouteAgreeOnAProvidersAddress(t *testing.T) {
	for _, c := range []struct{ provider, baseURL, want string }{
		{"mtplx", "http://127.0.0.1/v1", "http://" + mtplxServeHost},
		{"mtplx", "http://localhost", "http://localhost:" + strconv.Itoa(config.MtplxPort)},
		{"mtplx", "http://0.0.0.0/v1", "http://0.0.0.0:" + strconv.Itoa(config.MtplxPort)},
		{"mtplx", "http://localhost./v1", "http://localhost.:" + strconv.Itoa(config.MtplxPort)},
		{"mtplx", "http://127.0.0.1:9123/v1", "http://127.0.0.1:9123"},
		{"omlx", "http://127.0.0.1/v1", "http://127.0.0.1"},
		{"omlx", "http://127.0.0.1:9123/v1", "http://127.0.0.1:9123"},
		{"omlx", "https://omlx.example/v1", "https://omlx.example"},
		{"omlx-6bit", "http://localhost:8000", "http://localhost:8000"},
		{"ollama", "http://127.0.0.1", "http://127.0.0.1"},
		{"ollama", "http://127.0.0.1:9123", "http://127.0.0.1:9123"},
		{"ollama", "https://ollama.example", "https://ollama.example"},
		{"mlx_lm_server", "http://localhost/v1", "http://localhost"},
		{"mlx_lm_server", "http://localhost:8001/v1", "http://localhost:8001"},
	} {
		t.Run(c.provider+" "+c.baseURL, func(t *testing.T) {
			p := config.Provider{ID: c.provider, Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: c.baseURL}}
			m := config.Model{ID: c.provider + "/m", ProviderID: c.provider, ModelName: "m", Location: config.LocationLocal}
			cfg := &config.Config{Providers: []config.Provider{p}, Models: []config.Model{m}}
			family := localmodels.Family(c.provider)

			if probe, _ := localmodels.FamilyOrigin(cfg, family); probe != c.want {
				t.Errorf("probe origin = %q, want %q", probe, c.want)
			}

			switch family {
			case "mtplx":
				if origin, models, port := mtplxEndpoint(cfg); origin != c.want || models != c.want+"/v1/models" || !strings.HasSuffix(origin, ":"+strconv.Itoa(port)) {
					t.Errorf("start: origin %q, models url %q, --port %d; want %q", origin, models, port, c.want)
				}
			case "omlx", "ollama":
				e := testEnv()
				network := &servedAt{}
				network.on(e)
				e.lookPath = func(string) (string, error) { return "", os.ErrNotExist }
				if err := e.backends[family].start(context.Background(), e, cfg, Target{ProviderID: c.provider, ModelName: "m"}, func(Stage) {}); err == nil {
					t.Fatal("start succeeded against a network where nothing listens")
				}
				if got := network.asked(); !slices.Equal(got, []string{c.want}) {
					t.Errorf("start asked %v, want %q", got, c.want)
				}
			}

			node, err := litellm.BuildEntry(m, p)
			if err != nil {
				t.Fatalf("BuildEntry: %v", err)
			}
			var row struct {
				Params struct {
					APIBase string `yaml:"api_base"`
				} `yaml:"litellm_params"`
			}
			if err := node.Decode(&row); err != nil {
				t.Fatalf("decoding the route: %v", err)
			}
			if got := config.BaseOrigin(row.Params.APIBase); got != c.want {
				t.Errorf("route api_base = %q, an origin of %q; want %q", row.Params.APIBase, got, c.want)
			}

			route, err := cfg.ResolveRoute(m, []config.Protocol{config.ProtocolOpenAIChat})
			if err != nil {
				t.Fatalf("ResolveRoute: %v", err)
			}
			if route.Litellm || route.BaseOrigin != c.want {
				t.Errorf("direct route = %+v, want origin %q", route, c.want)
			}
		})
	}
}

// TestMtplxStartRefusesABaseURLWithNoPortItCannotServe verifies a start
// spawns nothing for an mtplx base_url that names no port and is not one wt
// gives a port to. wt serves mtplx on 127.0.0.1, so 8003 is put on a url only
// where that server answers. For any other url with no port the start used to
// put 8003 on it for itself alone, while the probes and the route read it as
// written: under an /etc/hosts alias the server came up, the check after the
// start asked port 80, and the start exited 1 with the server left running
// where the inventory never looks; for a remote host or [::1] wt spawned a
// local server and waited the whole load timeout on an address it is not at.
func TestMtplxStartRefusesABaseURLWithNoPortItCannotServe(t *testing.T) {
	for _, base := range []string{
		"http://mybox/v1", "http://10.0.0.5/v1", "https://localhost/v1",
		"http://[::1]/v1", "http://127.0.0.2/v1",
	} {
		t.Run(base, func(t *testing.T) {
			e, _, _, argvFile := mtplxEnv(t, "serve", 0)
			network := &servedAt{}
			network.on(e)
			cfg := provCfg("mtplx", base)
			err := (mtplxBackend{}).start(context.Background(), e, cfg, Target{ProviderID: "mtplx", ModelName: "Org/Model"}, func(Stage) {})
			var addr *MtplxAddressError
			if !errors.As(err, &addr) {
				t.Fatalf("start = %v, want *MtplxAddressError", err)
			}
			if want := config.BaseOrigin(base); addr.Origin != want {
				t.Errorf("the error names %q, want the registry's address %q", addr.Origin, want)
			}
			if _, statErr := os.Stat(argvFile); statErr == nil {
				t.Error("mtplx was spawned for a url wt cannot serve it at")
			}
			if got := network.asked(); len(got) != 0 {
				t.Errorf("the refused start asked %v, want nothing asked", got)
			}
		})
	}
}
