package cloudsync

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
)

// CatalogNameKey is the model-row key that records which page name a row was
// matched to.
const CatalogNameKey = "catalog_name"

// LibraryTagsURL is the page that lists the tags ollama publishes for a
// model; %s is the page name.
const LibraryTagsURL = "https://ollama.com/library/%s/tags"

const ollamaProvider = "ollama"

// tagLookups is how many library pages are read at once.
const tagLookups = 8

// Getter fetches one URL's body. A non-2xx answer is an error.
type Getter func(ctx context.Context, url string) ([]byte, error)

// CloudTag is the guess at a page name's pulled tag: `glm-5.3` gives
// `glm-5.3:cloud`, `gpt-oss:120b` gives `gpt-oss:120b-cloud`. Only a name
// that already pins a size is sure to be right; ResolveCloudTags checks the
// rest.
func CloudTag(name string) string {
	if strings.Contains(name, ":") {
		return name + "-cloud"
	}
	return name + ":cloud"
}

// IsCloudTag reports whether tag names an ollama cloud model.
func IsCloudTag(tag string) bool {
	return strings.HasSuffix(tag, ":cloud") || strings.HasSuffix(tag, "-cloud")
}

// VerifiedTags maps a page name to its tag for the catalog entries whose tag
// `ollama list` shows: a pulled tag exists, so it need not be looked up
// again.
//
// A name two pulled entries claim (an earlier re-tag that saved its addition
// and was killed before it removed the entry it replaced) is left out. Which
// of the two the page publishes is what the library lookup answers; answering
// it from the registry would pick one by position.
func VerifiedTags(entries []Entry, pulled []string) map[string]string {
	seen := map[string]string{}
	ambiguous := map[string]bool{}
	for _, e := range entries {
		if e.ProviderID != ollamaProvider || e.CatalogName == "" || !slices.Contains(pulled, e.ModelName) {
			continue
		}
		if tag, ok := seen[e.CatalogName]; ok && tag != e.ModelName {
			ambiguous[e.CatalogName] = true
		}
		seen[e.CatalogName] = e.ModelName
	}
	for name := range ambiguous {
		delete(seen, name)
	}
	return seen
}

// lookupCloudTag reads a bare page name's cloud tag off its library page:
// `<name>:cloud` wins, else the single `<name>:*-cloud` tag.
func lookupCloudTag(ctx context.Context, get Getter, name string) (tag, warning string) {
	body, err := get(ctx, fmt.Sprintf(LibraryTagsURL, name))
	if err != nil {
		return "", fmt.Sprintf("%s: could not read its ollama.com/library tags (%v); skipped", name, err)
	}
	link := regexp.MustCompile(`href="/library/` + regexp.QuoteMeta(name) + `:([A-Za-z0-9._-]+)"`)
	found := map[string]bool{}
	for _, m := range link.FindAllStringSubmatch(string(body), -1) {
		found[m[1]] = true
	}
	if found["cloud"] {
		return name + ":cloud", ""
	}
	var sized []string
	for t := range found {
		if strings.HasSuffix(t, "-cloud") {
			sized = append(sized, t)
		}
	}
	sort.Strings(sized)
	switch len(sized) {
	case 1:
		return name + ":" + sized[0], ""
	case 0:
		return "", name + ": no cloud tag on ollama.com/library; skipped"
	}
	return "", fmt.Sprintf("%s: several cloud tags (%s) on ollama.com/library; skipped", name, strings.Join(sized, ", "))
}

// ResolveCloudTags maps each page name to the cloud tag ollama publishes for
// it. "" means the tag is unknown: no cloud tag, several, or a library page
// that could not be read. Each of those comes with a warning, and none is
// ever a guess.
//
// A name that pins a size (`gpt-oss:120b`) maps by CloudTag with no lookup.
// A name in known (VerifiedTags) keeps its pulled tag. The rest are looked
// up, tagLookups at a time; the warnings come back in the order of names.
func ResolveCloudTags(ctx context.Context, get Getter, names []string, known map[string]string) (map[string]string, []string) {
	resolved := make(map[string]string, len(names))
	var lookups []string
	for _, name := range names {
		switch {
		case strings.Contains(name, ":"):
			resolved[name] = CloudTag(name)
		case known[name] != "":
			resolved[name] = known[name]
		default:
			lookups = append(lookups, name)
		}
	}
	tags := make([]string, len(lookups))
	warns := make([]string, len(lookups))
	var wg sync.WaitGroup
	slots := make(chan struct{}, tagLookups)
	for i, name := range lookups {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			tags[i], warns[i] = lookupCloudTag(ctx, get, name)
		}()
	}
	wg.Wait()
	var warnings []string
	for i, name := range lookups {
		resolved[name] = tags[i]
		if warns[i] != "" {
			warnings = append(warnings, warns[i])
		}
	}
	return resolved, warnings
}
