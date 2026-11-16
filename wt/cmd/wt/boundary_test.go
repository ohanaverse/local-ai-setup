package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/ohanaverse/local-ai-setup/wt"

// boundarySessionFiles are the session files TestSessionPathsNeverTouchTheRegistryOrLifecycle
// scans, relative to cmd/wt: the launch, resolve, smoke and profile paths of
// cmd/wt and every non-test file of internal/tui (except the host Models tab,
// models_tab*), internal/agents, internal/smoke, internal/profiles and
// internal/modelclient. A pattern that matches no file fails the test, so a
// renamed file cannot drop out of the scan unnoticed.
func boundarySessionFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, f := range []string{"launch", "resolve", "slot_launch", "smoke", "models", "resolve_remote", "launch_remote", "answerer", "job", "model_list_server", "app"} {
		if _, err := os.Stat(f + ".go"); err != nil {
			t.Fatalf("session file %s.go: %v", f, err)
		}
		out = append(out, f+".go")
	}
	globs := []string{
		"profile*.go",
		filepath.Join("..", "..", "internal", "tui", "*.go"),
		filepath.Join("..", "..", "internal", "agents", "*.go"),
		filepath.Join("..", "..", "internal", "smoke", "*.go"),
		filepath.Join("..", "..", "internal", "profiles", "*.go"),
		filepath.Join("..", "..", "internal", "modelclient", "*.go"),
	}
	for _, g := range globs {
		files, err := filepath.Glob(g)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") || strings.HasPrefix(filepath.Base(f), "models_tab") {
				continue
			}
			out = append(out, f)
			n++
		}
		if n == 0 {
			t.Fatalf("no session file matches %s", g)
		}
	}
	return out
}

// boundaryViolations parses one file and returns each banned selector or call
// as "file:line: func: what". Only selector expressions and calls are looked
// at, so a comment or a string naming a banned call is not a violation. A
// package selector (config.Load, lifecycle.Start, refcount.X, provider.X) is
// matched by the import path the file binds that local name to, so a local
// variable that happens to be called provider is not flagged. A method
// selector (.ResolveRoute, .IsLitellm, .LitellmBaseURL, .LitellmAPIKey) is
// matched by name on any receiver.
func boundaryViolations(fset *token.FileSet, path string, src any) ([]string, error) {
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	// local import name -> import path
	imports := map[string]string{}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		name := filepath.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		imports[name] = p
	}
	pkgOf := func(x ast.Expr) string {
		id, ok := x.(*ast.Ident)
		if !ok {
			return ""
		}
		p := imports[id.Name]
		if !strings.HasPrefix(p, modulePath+"/internal/") {
			return ""
		}
		return strings.TrimPrefix(p, modulePath+"/internal/")
	}
	bannedMethodCalls := map[string]bool{"ResolveRoute": true, "IsLitellm": true, "LitellmBaseURL": true, "LitellmAPIKey": true}
	var out []string
	report := func(n ast.Node, fn, what string) {
		out = append(out, fmt.Sprintf("%s:%d: %s: %s", path, fset.Position(n.Pos()).Line, fn, what))
	}
	for _, decl := range f.Decls {
		fn := "(package level)"
		if fd, ok := decl.(*ast.FuncDecl); ok {
			fn = fd.Name.Name
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				switch fun := n.Fun.(type) {
				case *ast.SelectorExpr:
					if pkgOf(fun.X) == "" && bannedMethodCalls[fun.Sel.Name] {
						report(n, fn, "."+fun.Sel.Name+"(")
					}
				case *ast.Ident:
					if bannedMethodCalls[fun.Name] {
						report(n, fn, fun.Name+"(")
					}
				}
			case *ast.SelectorExpr:
				name := n.Sel.Name
				switch pkgOf(n.X) {
				case "config":
					if name == "Load" {
						report(n, fn, "config.Load")
					}
				case "lifecycle":
					if name == "Start" || name == "EnsureModelRoute" || name == "WaitPendingRoutes" {
						report(n, fn, "lifecycle."+name)
					}
				case "localmodels":
					if name == "Inventory" {
						report(n, fn, "localmodels.Inventory")
					}
				case "refcount":
					report(n, fn, "refcount."+name)
				case "provider":
					report(n, fn, "provider."+name)
				}
			}
			return true
		})
	}
	return out, nil
}

// boundaryAllowed names the one exception: the host config loader. hostConfig
// (app.go) is the host commands' registry read (R3, R17); it reads it through
// loadHost, which holds the module's one config.Load a session file may name.
// protectedPaths (cleanup.go, R10) reaches the registry only through
// hostConfig, which the test checks separately.
var boundaryAllowed = map[string]bool{
	"app.go:loadHost:config.Load": true,
}

// TestSessionPathsNeverTouchTheRegistryOrLifecycle pins R3, R4 and R10 and the
// spec's anti-goal of a session with a model config of its own: after the
// flip every model a session uses comes from the model server, so no session
// file may load the registry (config.Load), resolve a direct route
// (.ResolveRoute, IsLitellm), read the host's LiteLLM url or key
// (LitellmBaseURL, LitellmAPIKey, R4), start or route a model in process
// (lifecycle.Start, EnsureModelRoute, WaitPendingRoutes), probe local
// inventory (localmodels.Inventory), keep a local refcount, or reach the
// provider table. It is a go/ast scan of selector expressions and calls, so
// comments and strings do not count. The allowed exceptions are the
// config.Load in app.go's hostConfig loader (the host commands' read) and
// protectedPaths (cleanup.go), which may reach the registry only through
// hostConfig; the test asserts both.
func TestSessionPathsNeverTouchTheRegistryOrLifecycle(t *testing.T) {
	fset := token.NewFileSet()
	var violations []string
	seenAllowed := map[string]bool{}
	for _, path := range boundarySessionFiles(t) {
		vs, err := boundaryViolations(fset, path, nil)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, v := range vs {
			// v is "path:line: func: what"
			parts := strings.SplitN(v, ": ", 3)
			file := strings.SplitN(parts[0], ":", 2)[0]
			key := file + ":" + parts[1] + ":" + parts[2]
			if boundaryAllowed[key] {
				seenAllowed[key] = true
				continue
			}
			violations = append(violations, v)
		}
	}
	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("session path touches the registry, a direct route or the lifecycle: %s", v)
	}
	for key := range boundaryAllowed {
		if !seenAllowed[key] {
			t.Errorf("allowed exception %s not found: update boundaryAllowed so it names the real host loader", key)
		}
	}

	// protectedPaths (R10) reads the registry only through hostConfig.
	f, err := parser.ParseFile(fset, "cleanup.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	found, viaHost := false, false
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "protectedPaths" {
			continue
		}
		found = true
		ast.Inspect(fd, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "config" && sel.Sel.Name == "Load" {
				t.Errorf("protectedPaths calls config.Load directly; it must read the registry through hostConfig (R10)")
			}
			if sel.Sel.Name == "hostConfig" {
				viaHost = true
			}
			return true
		})
	}
	if !found {
		t.Fatal("cleanup.go has no protectedPaths: update the R10 exception")
	}
	if !viaHost {
		t.Error("protectedPaths no longer reads the registry through hostConfig (R10)")
	}
}

// TestBoundaryScanSeesOnlyCode guards the scanner itself: a banned name in a
// comment or a string is not a violation, a local variable called provider is
// not the provider package, and a real call through an import alias is.
func TestBoundaryScanSeesOnlyCode(t *testing.T) {
	src := `package x

import (
	cfgpkg "` + modulePath + `/internal/config"
	"` + modulePath + `/internal/refcount"
)

// config.Load() and refcount.Open in a comment.
func f(provider struct{ Name string }) string {
	_ = "lifecycle.Start( cfg.ResolveRoute( provider.Lookup"
	_, _ = cfgpkg.Load()
	_ = refcount.Store{}
	return provider.Name
}
`
	vs, err := boundaryViolations(token.NewFileSet(), "x.go", src)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"x.go:11: f: config.Load", "x.go:12: f: refcount.Store"}
	if strings.Join(vs, "\n") != strings.Join(want, "\n") {
		t.Errorf("violations = %q, want %q", vs, want)
	}
}
