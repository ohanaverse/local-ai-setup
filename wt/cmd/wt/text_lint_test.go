package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// sessionWordFiles are the files TestUserTextQualifiesSession reads: every
// non-test file of internal/modelclient, and the cmd/wt files the client path
// added (Tasks 10-17) whose whole text is the client's.
func sessionWordFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "internal", "modelclient", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range files {
		if !strings.HasSuffix(f, "_test.go") {
			out = append(out, f)
		}
	}
	for _, f := range []string{"models", "resolve_remote", "launch_remote", "answerer", "job", "model_list_server", "model_cmds_remote"} {
		out = append(out, f+".go")
	}
	return out
}

// sessionWord finds "session" (any case, any plural) in a literal.
var sessionWord = regexp.MustCompile(`(?i)session`)

// unqualifiedSession reports the first "session" in s that is not preceded by
// "client " or "wt " (case-insensitive), and whether there is one.
func unqualifiedSession(s string) (int, bool) {
	low := strings.ToLower(s)
	for _, loc := range sessionWord.FindAllStringIndex(s, -1) {
		before := low[:loc[0]]
		if strings.HasSuffix(before, "client ") || strings.HasSuffix(before, "wt ") {
			continue
		}
		return loc[0], true
	}
	return 0, false
}

// TestUserTextQualifiesSession pins N7 (the plan's wording rule): the user
// text of the model server's client says "client session" wherever it means
// the server's registry of clients, because "wt session" already names a
// launch that claims a worktree, and a bare "session" could be either. It
// parses internal/modelclient and the client files of cmd/wt and fails,
// case-insensitively, on any string literal holding "session" not preceded by
// "client " or "wt ". A literal that starts with "/" (a URL path such as
// "/control/v1/sessions"), one holding "%" (a format the caller completes)
// and a struct tag (a JSON key) are not user text and are skipped, as the
// plan's ruling on this test says.
func TestUserTextQualifiesSession(t *testing.T) {
	fset := token.NewFileSet()
	for _, path := range sessionWordFiles(t) {
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		skip := map[*ast.BasicLit]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Field:
				if n.Tag != nil {
					skip[n.Tag] = true
				}
			case *ast.ImportSpec:
				skip[n.Path] = true
			}
			return true
		})
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || skip[lit] {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if strings.HasPrefix(s, "/") {
				return true
			}
			if i, bad := unqualifiedSession(s); bad {
				t.Errorf("%s: %q: unqualified %q at %d; write \"client session\" (N7)", fset.Position(lit.Pos()), s, s[i:min(len(s), i+8)], i)
			}
			return true
		})
	}
}
