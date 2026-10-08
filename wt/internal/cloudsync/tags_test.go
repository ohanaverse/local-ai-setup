package cloudsync

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// tagsPage is a library tags page listing the given tags for name, plus a
// link to another model whose name starts the same way.
func tagsPage(name string, tags ...string) string {
	var b strings.Builder
	b.WriteString("<html>")
	for _, t := range tags {
		fmt.Fprintf(&b, `<a href="/library/%s:%s">%s:%s</a>`, name, t, name, t)
	}
	fmt.Fprintf(&b, `<a href="/library/%s-other:cloud">x</a></html>`, name)
	return b.String()
}

// fakeLibrary serves pages by URL and records which URLs were asked for. A
// URL with no page is a 404.
type fakeLibrary struct {
	mu    sync.Mutex
	pages map[string]string
	asked []string
}

func (l *fakeLibrary) get(_ context.Context, url string) ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.asked = append(l.asked, url)
	page, ok := l.pages[url]
	if !ok {
		return nil, errors.New("HTTP 404")
	}
	return []byte(page), nil
}

// TestCloudTag pins the guess and the cloud-tag test. IsCloudTag decides
// which pulled tags and registry entries the catalog flow may remove, so a
// local model such as gpt-oss:20b must never pass it.
func TestCloudTag(t *testing.T) {
	if got := CloudTag("glm-5.3"); got != "glm-5.3:cloud" {
		t.Errorf("CloudTag(glm-5.3) = %q", got)
	}
	if got := CloudTag("gpt-oss:120b"); got != "gpt-oss:120b-cloud" {
		t.Errorf("CloudTag(gpt-oss:120b) = %q", got)
	}
	if !IsCloudTag("glm-5.3:cloud") || !IsCloudTag("gpt-oss:120b-cloud") || IsCloudTag("gpt-oss:20b") {
		t.Error("IsCloudTag: want true for :cloud and -cloud tags, false for gpt-oss:20b")
	}
}

// TestResolveCloudTags pins how a page name becomes the tag ollama really
// publishes: the :cloud alias wins, else the single sized cloud tag; several
// sized tags, none, or an unreadable page give no tag and a warning. A
// guessed tag would be registered, fail to pull, and be removed and re-added
// on every run.
func TestResolveCloudTags(t *testing.T) {
	if LibraryTagsURL != "https://ollama.com/library/%s/tags" {
		t.Fatalf("LibraryTagsURL = %q", LibraryTagsURL)
	}
	url := func(name string) string { return fmt.Sprintf(LibraryTagsURL, name) }
	lib := &fakeLibrary{pages: map[string]string{
		url("glm-5.3"):         tagsPage("glm-5.3", "cloud", "latest"),
		url("mistral-large-3"): tagsPage("mistral-large-3", "675b-cloud", "latest"),
		url("two"):             tagsPage("two", "8b-cloud", "70b-cloud"),
		url("none"):            tagsPage("none", "latest", "8b"),
	}}
	names := []string{"glm-5.3", "mistral-large-3", "two", "none", "gone", "gpt-oss:120b"}
	resolved, warnings := ResolveCloudTags(context.Background(), lib.get, names, nil)
	want := map[string]string{
		"glm-5.3":         "glm-5.3:cloud",
		"mistral-large-3": "mistral-large-3:675b-cloud",
		"two":             "",
		"none":            "",
		"gone":            "",
		// A page name that already names a size pins its tag; no lookup.
		"gpt-oss:120b": "gpt-oss:120b-cloud",
	}
	if !reflect.DeepEqual(resolved, want) {
		t.Errorf("resolved = %v\nwant %v", resolved, want)
	}
	wantWarnings := []string{
		"two: several cloud tags (70b-cloud, 8b-cloud) on ollama.com/library; skipped",
		"none: no cloud tag on ollama.com/library; skipped",
		"gone: could not read its ollama.com/library tags (HTTP 404); skipped",
	}
	if !reflect.DeepEqual(warnings, wantWarnings) {
		t.Errorf("warnings = %q\nwant %q", warnings, wantWarnings)
	}
	if len(lib.asked) != 5 {
		t.Errorf("looked up %d pages (%v), want 5: every bare name and not gpt-oss:120b", len(lib.asked), lib.asked)
	}
}

// TestResolveCloudTagsSkipsKnownNames pins that a name whose tag is already
// pulled is not looked up again. A routine sync would otherwise fetch one
// library page per model on every run, and fail as a whole when ollama.com's
// library is down although nothing needs resolving.
func TestResolveCloudTagsSkipsKnownNames(t *testing.T) {
	boom := func(_ context.Context, url string) ([]byte, error) {
		t.Errorf("fetched %s", url)
		return nil, errors.New("no")
	}
	resolved, warnings := ResolveCloudTags(context.Background(), boom, []string{"ml3"}, map[string]string{"ml3": "ml3:675b-cloud"})
	if resolved["ml3"] != "ml3:675b-cloud" || len(warnings) != 0 {
		t.Errorf("resolved = %v, warnings = %q", resolved, warnings)
	}
}

// TestResolveCloudTagsKeepsOrderUnderConcurrency pins that the lookups, which
// run several at a time, still give warnings in the order of the page. The
// plan prints them, and the command compares the printed plan with a re-plan:
// warnings in a varying order would make every apply look like a changed plan.
func TestResolveCloudTagsKeepsOrderUnderConcurrency(t *testing.T) {
	var names, want []string
	for i := range 40 {
		name := fmt.Sprintf("m%02d", i)
		names = append(names, name)
		want = append(want, name+": could not read its ollama.com/library tags (HTTP 404); skipped")
	}
	lib := &fakeLibrary{}
	for range 5 {
		if _, warnings := ResolveCloudTags(context.Background(), lib.get, names, nil); !reflect.DeepEqual(warnings, want) {
			t.Fatalf("warnings out of page order: %q", warnings)
		}
	}
}

// TestVerifiedTags pins which registry tags are trusted without a lookup:
// only an entry that records its catalog name and whose tag is pulled. An
// unpulled tag may be an earlier guess, and trusting it would keep the guess
// alive forever.
func TestVerifiedTags(t *testing.T) {
	entries := []Entry{
		{ID: "ollama/a:1t-cloud", ProviderID: "ollama", ModelName: "a:1t-cloud", CatalogName: "a"},
		{ID: "ollama/b:cloud", ProviderID: "ollama", ModelName: "b:cloud", CatalogName: "b"}, // not pulled
		{ID: "ollama/c:cloud", ProviderID: "ollama", ModelName: "c:cloud"},                   // no catalog name
		{ID: "other/d:cloud", ProviderID: "other", ModelName: "d:cloud", CatalogName: "d"},   // not ollama
	}
	got := VerifiedTags(entries, []string{"a:1t-cloud", "c:cloud", "d:cloud"})
	if want := map[string]string{"a": "a:1t-cloud"}; !reflect.DeepEqual(got, want) {
		t.Errorf("VerifiedTags = %v, want %v", got, want)
	}
}

// TestVerifiedTagsSkipsANameTwoPulledEntriesClaim pins the interrupted
// re-tag: the guessed tag and the real one are both pulled and both carry the
// name. Answering from the registry picks one by position; leaving it out
// hands the question to the library lookup, so the current entry is matched
// and the stale one is what gets re-tagged away.
func TestVerifiedTagsSkipsANameTwoPulledEntriesClaim(t *testing.T) {
	entries := []Entry{
		{ID: "ollama/foo:cloud", ProviderID: "ollama", ModelName: "foo:cloud", CatalogName: "foo"},
		{ID: "ollama/foo:675b-cloud", ProviderID: "ollama", ModelName: "foo:675b-cloud", CatalogName: "foo"},
		{ID: "ollama/bar:cloud", ProviderID: "ollama", ModelName: "bar:cloud", CatalogName: "bar"},
	}
	got := VerifiedTags(entries, []string{"foo:cloud", "foo:675b-cloud", "bar:cloud"})
	if want := map[string]string{"bar": "bar:cloud"}; !reflect.DeepEqual(got, want) {
		t.Errorf("VerifiedTags = %v, want %v", got, want)
	}
}
