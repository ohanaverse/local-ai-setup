// wt/internal/profiles/merge.go
package profiles

// mergeInto recursively merges src into dst: a key present in both whose
// values are themselves map[string]any is merged key-by-key instead of
// replaced wholesale, so a profile's config_content can add or override
// one nested field (e.g. config_content.env.SOME_KEY, or opencode's own
// config_content.provider.agent-wt.<field>) without clobbering sibling
// fields already present at that same nested key. A non-map value on
// either side — including a src map colliding with a non-map dst value —
// is whole-value replacement, matching TOML's own "last value wins"
// semantics for scalars and lists.
func mergeInto(dst, src map[string]any) {
	for k, v := range src {
		if srcMap, ok := v.(map[string]any); ok {
			if dstMap, ok := dst[k].(map[string]any); ok {
				mergeInto(dstMap, srcMap)
				continue
			}
		}
		dst[k] = v
	}
}
