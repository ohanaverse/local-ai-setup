package localmodels

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// sizedOllamaServer serves /api/tags and /api/ps with a size per name: what
// ollama reports as bytes on disk on the first and bytes in memory on the
// second.
func sizedOllamaServer(t *testing.T, pulled, loaded map[string]int64) *httptest.Server {
	t.Helper()
	body := func(sizes map[string]int64) map[string]any {
		ms := []map[string]any{}
		for n, s := range sizes {
			ms = append(ms, map[string]any{"name": n, "size": s})
		}
		return map[string]any{"models": ms}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(body(pulled)) })
	mux.HandleFunc("/api/ps", func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(body(loaded)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestOllamaLoadedKeepsSizes pins that OllamaLoaded returns each loaded
// model's /api/ps size beside its name. The start warning about other
// providers' models (#412) prints that figure as the memory an ollama model
// holds; a reader that kept names only would print "memory use unknown" for
// every ollama model.
func TestOllamaLoadedKeepsSizes(t *testing.T) {
	srv := sizedOllamaServer(t, nil, map[string]int64{"qwen3:8b": 9_600_000_000})
	got, err := OllamaLoaded(context.Background(), testClient, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "qwen3:8b" || got[0].Size != 9_600_000_000 {
		t.Errorf("OllamaLoaded = %+v, want qwen3:8b with 9600000000 bytes", got)
	}
}

// TestInventoryOllamaLoadedSizeFromPs pins the two ollama figures apart: the
// /api/ps size is what a loaded model holds in memory (Entry.LoadedSize), the
// /api/tags size is the download on disk (Entry.Size). Swapping them would
// tell a user an ollama model holds its file size in memory, or `wt model
// list` would print a memory figure as the size on disk. A model that is only
// pulled holds no memory, so its LoadedSize stays 0.
func TestInventoryOllamaLoadedSizeFromPs(t *testing.T) {
	srv := sizedOllamaServer(t,
		map[string]int64{"qwen3:8b": 5_200_000_000, "gemma4:2b": 1_700_000_000},
		map[string]int64{"qwen3:8b": 9_600_000_000})
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("ollama", srv.URL, "")},
		Models:    []config.Model{{ID: "ollama/qwen3:8b", ProviderID: "ollama", ModelName: "qwen3:8b", Location: config.LocationLocal}},
	}
	snap := inventory(cfg, testClient)

	q, ok := byModelID(snap, "ollama/qwen3:8b")
	if !ok || !q.Running || q.Size != 5_200_000_000 || q.LoadedSize != 9_600_000_000 {
		t.Errorf("qwen3:8b = %+v ok=%v, want running, Size 5200000000 (tags), LoadedSize 9600000000 (ps)", q, ok)
	}
	g, ok := byModelID(snap, config.DiscoveredModelID("ollama", "gemma4:2b"))
	if !ok || g.Running || g.Size != 1_700_000_000 || g.LoadedSize != 0 {
		t.Errorf("gemma4:2b = %+v ok=%v, want pulled only: Size 1700000000, LoadedSize 0", g, ok)
	}
}

// TestInventoryOmlxLoadedSizeOnlyWhenSizesKnown pins omlx's figure: a loaded
// model's LoadedSize is the pool's resident estimate when omlx's status
// reading answered, and 0 (unknown) when omlx answered only through the
// fallback reading (an omlx with no status endpoint), which carries no
// sizes, and for a model that is not
// loaded. Reporting a figure the reading does not hold would tell the user a
// model holds no memory.
func TestInventoryOmlxLoadedSizeOnlyWhenSizesKnown(t *testing.T) {
	dir := t.TempDir()
	mkOmlxModels(t, dir, "A", "B")
	models := []config.Model{{ID: "omlx/A", ProviderID: "omlx", ModelName: "A"}}

	srv := &fakeOmlx{listed: []string{"A", "B"}, pool: map[string]bool{"A": true, "B": false}, sizes: map[string]int64{"A": 18_200_000_000, "B": 4_000_000_000}}
	snap := inventory(&config.Config{Providers: []config.Provider{localProvider("omlx", srv.serve(t), dir)}, Models: models}, testClient)
	a, _ := byModelID(snap, "omlx/A")
	b, _ := byModelID(snap, "omlx/B")
	if !a.Running || a.LoadedSize != 18_200_000_000 {
		t.Errorf("omlx/A = %+v, want running with LoadedSize 18200000000", a)
	}
	if b.Running || b.LoadedSize != 0 {
		t.Errorf("omlx/B = %+v, want not running with LoadedSize 0", b)
	}

	old := &fakeOmlx{listed: []string{"A"}, pool: map[string]bool{"A": true}, sizes: map[string]int64{"A": 18_200_000_000}, noStatus: true}
	snap = inventory(&config.Config{Providers: []config.Provider{localProvider("omlx", old.serve(t), dir)}, Models: models}, testClient)
	if snap.OmlxPool == nil || snap.OmlxPool.SizesKnown {
		t.Fatalf("pool = %+v, want the fallback reading", snap.OmlxPool)
	}
	if a, _ := byModelID(snap, "omlx/A"); !a.Running || a.LoadedSize != 0 {
		t.Errorf("fallback omlx/A = %+v, want running with LoadedSize 0 (unknown)", a)
	}
}

// TestFormatSize pins the sizes the listing, the Models tab and the start
// warning about other providers' models print: decimal units as `ollama list`
// shows them, and "-" for a size wt does not know, never "0 B".
func TestFormatSize(t *testing.T) {
	for n, want := range map[int64]string{
		0: "-", -1: "-", 7: "7 B", 4_200: "4 kB", 734_000_000: "734 MB", 5_225_388_164: "5.2 GB", 27_000_000_000: "27.0 GB",
		// Just under a unit: the next unit, never "1000" of this one.
		999_700: "1 MB", 999_700_000: "1.0 GB",
	} {
		if got := FormatSize(n); got != want {
			t.Errorf("FormatSize(%d) = %q, want %q", n, got, want)
		}
	}
}
