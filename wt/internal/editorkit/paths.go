package editorkit

import "strings"

// TildePath writes a path under the home directory with ~ for the home, the
// way the registry's own model_dir does: shorter, and still a path a shell
// takes.
func TildePath(path, home string) string {
	if home == "" || home == "/" {
		return path
	}
	if rest, ok := strings.CutPrefix(path, home); ok && (rest == "" || strings.HasPrefix(rest, "/")) {
		return "~" + rest
	}
	return path
}

// HomePath writes a path under home with ~ for the home.
//
// The token file is shown in this one form whatever the config wrote: Observe
// hands back the expanded path (State.Token.Path), which is the file a
// session reads, while [model-server] token_file may say ~/... or the same
// path in full. Abbreviating the expanded path against the home Observe used
// gives the ~ form a user writes, is the same file the line's ok / loose /
// missing is about, and stays one form when the value came from
// WT_MODEL_TOKEN_FILE or the agent's plist instead of the table.
func HomePath(path, stateHome, userHome string) string {
	if stateHome == "" || stateHome == "/" {
		return TildePath(path, userHome)
	}
	if rest, ok := strings.CutPrefix(path, stateHome); ok && (rest == "" || strings.HasPrefix(rest, "/")) {
		return "~" + rest
	}
	return path
}

// BreakPath splits a path into lines no wider than width, breaking only
// after a "/"; a segment wider than the width on its own is cut at the width.
func BreakPath(path string, width int) []string {
	if width <= 0 || len(path) <= width {
		return []string{path}
	}
	var segs []string
	for rest := path; rest != ""; {
		i := strings.IndexByte(rest, '/')
		if i < 0 {
			segs, rest = append(segs, rest), ""
			continue
		}
		segs, rest = append(segs, rest[:i+1]), rest[i+1:]
	}
	var out []string
	cur := ""
	for _, seg := range segs {
		for len(seg) > width {
			if cur != "" {
				out, cur = append(out, cur), ""
			}
			out, seg = append(out, seg[:width]), seg[width:]
		}
		if len(cur)+len(seg) > width {
			out, cur = append(out, cur), ""
		}
		cur += seg
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
