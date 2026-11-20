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
// scans, relative to cmd/wt-session: the launch, resolve, smoke and profile paths of
// cmd/wt-session (main.go's root command and runLaunchPath, the model-server
// start/stop/job commands of model_cmds.go (startCmd and the session
// stopCmd) and model_cmds_remote.go, commands.go's rotate and
// the shared helpers.go included) and every non-test file of internal/tui
// (except the host Models tab, models_tab*), internal/agents, internal/smoke,
// internal/profiles and internal/modelclient. A pattern that matches no file fails the test, so a
// renamed file cannot drop out of the scan unnoticed.
func boundarySessionFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, f := range []string{"launch", "resolve", "slot_launch", "smoke", "models", "resolve_remote", "launch_remote", "answerer", "job", "model_list_server", "app", "main", "model_cmds", "model_cmds_remote", "commands", "helpers"} {
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
// as "file:line: func: what". Only imports, selector expressions and calls are
// looked at, so a comment or a string naming a banned call is not a
// violation. A package selector (config.Load, lifecycle.Start, refcount.X,
// provider.X) is matched by the import path the file binds that local name
// to, so a local variable that happens to be called provider is not flagged.
// A method selector (.ResolveRoute, .IsLitellm, .LitellmBaseURL,
// .LitellmAPIKey, and the host's registry wrapper .hostConfig and
// .hostConfigErr, R3) is matched by name on any receiver, called or used as a
// method value. A dot-import of a banned package is a violation of its own:
// its names would be bare identifiers the selector match cannot see.
func boundaryViolations(fset *token.FileSet, path string, src any) ([]string, error) {
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	// local import name -> import path
	imports := map[string]string{}
	var out []string
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		name := filepath.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if rel := strings.TrimPrefix(p, modulePath+"/"); name == "." && boundaryBannedPkgs[rel] {
			out = append(out, fmt.Sprintf("%s:%d: (package level): dot-import of %s", path, fset.Position(imp.Pos()).Line, rel))
			continue
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
	bannedMethods := map[string]bool{"ResolveRoute": true, "IsLitellm": true, "LitellmBaseURL": true, "LitellmAPIKey": true, "hostConfig": true, "hostConfigErr": true}
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
				if fun, ok := n.Fun.(*ast.Ident); ok && bannedMethods[fun.Name] {
					report(n, fn, fun.Name+"(")
				}
			case *ast.SelectorExpr:
				name := n.Sel.Name
				if pkgOf(n.X) == "" && bannedMethods[name] {
					// Called or not: a method value reaches as far.
					report(n, fn, "."+name)
				}
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

// boundaryBannedPkgs are the internal packages a session file may not
// dot-import (relative to the module path).
var boundaryBannedPkgs = map[string]bool{
	"internal/config": true, "internal/lifecycle": true, "internal/localmodels": true,
	"internal/refcount": true, "internal/provider": true,
}

// boundaryAllowed names the exceptions, each with its reason:
//
//   - app.go:loadHost:config.Load — hostConfig (app.go) is the host commands'
//     registry read (R3, R17); it reads it through loadHost, which holds the
//     module's one config.Load a session file may name. protectedPaths
//     (cleanup.go, R10) reaches the registry only through hostConfig, which
//     the test checks separately.
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
	problems, err := protectedPathsProblems(fset, "cleanup.go", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// protectedPathsProblems checks cleanup.go's protectedPaths (R10): it must
// exist, must call hostConfig on one of its own parameters (or its receiver)
// and must not select Load from the config package, whatever local name the
// file binds that import to.
func protectedPathsProblems(fset *token.FileSet, path string, src any) ([]string, error) {
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	configNames := map[string]bool{}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if p != modulePath+"/internal/config" {
			continue
		}
		name := "config"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		configNames[name] = true
	}
	var problems []string
	found, viaHost := false, false
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "protectedPaths" {
			continue
		}
		found = true
		own := map[string]bool{}
		for _, fl := range []*ast.FieldList{fd.Recv, fd.Type.Params} {
			if fl == nil {
				continue
			}
			for _, field := range fl.List {
				for _, n := range field.Names {
					own[n.Name] = true
				}
			}
		}
		ast.Inspect(fd, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				if id, ok := n.X.(*ast.Ident); ok && configNames[id.Name] && n.Sel.Name == "Load" {
					problems = append(problems, "protectedPaths calls config.Load directly; it must read the registry through hostConfig (R10)")
				}
			case *ast.CallExpr:
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "hostConfig" {
					if id, ok := sel.X.(*ast.Ident); ok && own[id.Name] {
						viaHost = true
					}
				}
			}
			return true
		})
	}
	if !found {
		return []string{path + " has no protectedPaths: update the R10 exception"}, nil
	}
	if !viaHost {
		problems = append(problems, "protectedPaths no longer reads the registry through hostConfig (R10)")
	}
	return problems, nil
}

// TestProtectedPathsCheckResolvesTheImport pins the R10 check itself: a
// config.Load reached through an import alias is still a direct registry
// read, and a hostConfig that is only a method value, or is called on
// something other than protectedPaths' own app, does not count as reading
// through it. Without it renaming the import would blind the check.
func TestProtectedPathsCheckResolvesTheImport(t *testing.T) {
	src := `package x

import cfgx "` + modulePath + `/internal/config"

func protectedPaths(a *app) {
	_, _ = cfgx.Load()
	_ = a.hostConfig
	_, _ = other.hostConfig()
}
`
	got, err := protectedPathsProblems(token.NewFileSet(), "x.go", src)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"protectedPaths calls config.Load directly; it must read the registry through hostConfig (R10)",
		"protectedPaths no longer reads the registry through hostConfig (R10)",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("problems = %q, want %q", got, want)
	}
}

// TestBoundaryScanSeesOnlyCode guards the scanner itself: a banned name in a
// comment or a string is not a violation, a local variable called provider in
// a file that does not import the provider package is not that package, and
// every kind of banned use is one: a call or a
// selector through an import alias (one per banned package, a package-level
// `var _ = lc.X` included), a provider type, a banned method used as a value
// rather than called, a call of the host config wrapper (hostConfig,
// hostConfigErr) and a dot-import of a banned package. Without it a scanner
// regression would let TestSessionPathsNeverTouchTheRegistryOrLifecycle pass
// over a real violation.
func TestBoundaryScanSeesOnlyCode(t *testing.T) {
	src := `package x

import (
	cfgpkg "` + modulePath + `/internal/config"
	lc "` + modulePath + `/internal/lifecycle"
	lm "` + modulePath + `/internal/localmodels"
	"` + modulePath + `/internal/provider"
	"` + modulePath + `/internal/refcount"
	. "` + modulePath + `/internal/localmodels"
)

// config.Load() and refcount.Open in a comment.
var _ = lc.WaitPendingRoutes

func f(a *app, p struct{ Name string }) string {
	_ = "lifecycle.Start( cfg.ResolveRoute( provider.Lookup"
	_, _ = cfgpkg.Load()
	_ = refcount.Store{}
	_ = lc.Start
	lc.EnsureModelRoute()
	_ = lm.Inventory
	var _ provider.Family
	key := a.cfg.LitellmAPIKey
	_, _ = a.hostConfig()
	_ = a.hostConfigErr()
	_ = key
	return p.Name
}
`
	vs, err := boundaryViolations(token.NewFileSet(), "x.go", src)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"x.go:9: (package level): dot-import of internal/localmodels",
		"x.go:13: (package level): lifecycle.WaitPendingRoutes",
		"x.go:17: f: config.Load",
		"x.go:18: f: refcount.Store",
		"x.go:19: f: lifecycle.Start",
		"x.go:20: f: lifecycle.EnsureModelRoute",
		"x.go:21: f: localmodels.Inventory",
		"x.go:22: f: provider.Family",
		"x.go:23: f: .LitellmAPIKey",
		"x.go:24: f: .hostConfig",
		"x.go:25: f: .hostConfigErr",
	}
	if strings.Join(vs, "\n") != strings.Join(want, "\n") {
		t.Errorf("violations =\n%s\nwant\n%s", strings.Join(vs, "\n"), strings.Join(want, "\n"))
	}

	// A file that does not import the provider package: a local variable
	// called provider, and its field, are no violation.
	local := `package x

func g(provider struct{ Name string }) string {
	_ = "provider.Lookup in a string"
	return provider.Name
}
`
	if vs, err := boundaryViolations(token.NewFileSet(), "y.go", local); err != nil || len(vs) != 0 {
		t.Errorf("a local variable named provider: violations = %q (err %v), want none", vs, err)
	}
}
