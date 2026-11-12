package cleanup

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// lossCounts is what one function does with a row's losses, by kind:
//
//   - raw: a `.Losses` read carrying the marker `// losses: raw, <why>`, as a
//     trailing comment on its own line or alone on the line just before it
//     (a deliberate read of the raw value); a trailing marker never covers
//     the next line, a standalone one never its own or a later line;
//   - effective: a call of `EffectiveLosses` (the view a removal reads);
//   - seal: a read of an Item's `SawLosses` (or the seal's `sawLosses`): the
//     value ItemFromRow sealed, which is the effective view;
//   - write: a `.Losses` on the left of an assignment (the classifier
//     building the raw value);
//   - unlabelled: a `.Losses` read with no marker. Never allowed: whoever adds
//     one decides whether it is raw (and says why next to it) or effective.
type lossCounts struct{ raw, effective, seal, write, unlabelled int }

// lossSites is every function in cmd/wt and internal/ (non-test files) that
// touches a row's losses, keyed "<file relative to the module root>#<func>"
// with the receiver type for a method. The counts are exact, so a second
// read added to a listed function fails too. A preserved row's raw losses
// still count its covered commits, and its covered dirt (CoversDirt); only the
// sites labelled raw may see them.
var lossSites = map[string]lossCounts{
	// The classifier builds the raw value; the view is made from it.
	"internal/cleanup/classify.go#classifier.worktree":  {write: 7},
	"internal/cleanup/classify.go#classifier.branchRow": {write: 3},
	"internal/cleanup/classify.go#Row.EffectiveLosses":  {raw: 1},
	"internal/cleanup/present.go#LossLines":             {effective: 1},
	"internal/cleanup/present.go#RowTags":               {effective: 1},

	// The removal engine: approvals, the lapse check and the seal read the
	// effective view; two gates read raw values the view never changes or
	// git needs as they are.
	"internal/removal/engine.go#needsOf":             {effective: 1},
	"internal/removal/engine.go#unapproved":          {effective: 1},
	"internal/removal/engine.go#Engine.worktreeGate": {raw: 2, effective: 1, seal: 1},
	"internal/removal/engine.go#Engine.branchGate":   {effective: 1, seal: 1},
	// The re-check right before git for covered dirt: the gate's lapse
	// comparison again, and --force from the raw Dirty.
	"internal/removal/engine.go#coverLapsed":          {raw: 1, effective: 1, seal: 1},
	"internal/removal/engine.go#Engine.worktreeAct":   {raw: 1},
	"internal/removal/preview.go#ItemFromRow":         {effective: 1},
	"internal/removal/preview.go#ItemPreview.confirm": {seal: 1},
	"internal/removal/types.go#sealOf":                {seal: 1},

	// Deliberately raw: the exit prompt never offers a row that would lose
	// anything, and rescue must still see (and could rescue again) a
	// preserved original's commits.
	"internal/exitprompt/exitprompt.go#Eligible":    {raw: 1},
	"internal/rescue/preflight.go#Engine.Preflight": {raw: 8},
	"internal/rescue/offer.go#Offered":              {raw: 4},

	// Deliberately raw: the launch warning's dirt is what is physically in
	// the worktree, whatever a rescue record covers.
	"internal/launchwarn/row.go#FromRow": {raw: 1},
}

// rawMarker starts the comment a raw read carries.
const rawMarker = "// losses: raw, "

// funcKey is a function declaration's key: Name, or Recv.Name for a method.
func funcKey(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	t := fd.Recv.List[0].Type
	for {
		switch x := t.(type) {
		case *ast.StarExpr:
			t = x.X
			continue
		case *ast.IndexExpr:
			t = x.X
			continue
		case *ast.IndexListExpr:
			t = x.X
			continue
		}
		break
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}

// scanLosses returns, per function of file (rel is its key prefix), what it
// does with losses. A selector on an imported package name (cleanup.Losses
// as a type) is not a use.
func scanLosses(fset *token.FileSet, file *ast.File, rel string) map[string]lossCounts {
	pkgs := map[string]bool{}
	for _, im := range file.Imports {
		p, _ := strconv.Unquote(im.Path.Value)
		name := path.Base(p)
		if im.Name != nil {
			name = im.Name.Name
		}
		pkgs[name] = true
	}
	// A line holding code before a comment makes that comment trailing.
	codeBefore := map[int]token.Pos{} // line -> the earliest code position on it
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			return true
		}
		if _, ok := n.(*ast.CommentGroup); ok {
			return false
		}
		if _, ok := n.(*ast.Comment); ok {
			return false
		}
		for _, p := range []token.Pos{n.Pos(), n.End() - 1} {
			if !p.IsValid() {
				continue
			}
			l := fset.Position(p).Line
			if q, ok := codeBefore[l]; !ok || p < q {
				codeBefore[l] = p
			}
		}
		return true
	})
	// The lines a raw marker covers: a trailing marker its own line only, a
	// marker alone on its line the next line only.
	marked := map[int]bool{}
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			if strings.HasPrefix(c.Text, rawMarker) && strings.TrimSpace(strings.TrimPrefix(c.Text, rawMarker)) != "" {
				l := fset.Position(c.Slash).Line
				if q, ok := codeBefore[l]; ok && q < c.Slash {
					marked[l] = true
				} else {
					marked[l+1] = true
				}
			}
		}
	}
	out := map[string]lossCounts{}
	for _, d := range file.Decls {
		key := rel + "#<package>"
		if fd, ok := d.(*ast.FuncDecl); ok {
			key = rel + "#" + funcKey(fd)
		}
		writes := map[*ast.SelectorExpr]bool{}
		ast.Inspect(d, func(n ast.Node) bool {
			var lhs []ast.Expr
			switch s := n.(type) {
			case *ast.AssignStmt:
				lhs = s.Lhs
			case *ast.IncDecStmt:
				lhs = []ast.Expr{s.X}
			}
			for _, e := range lhs {
				for {
					sel, ok := e.(*ast.SelectorExpr)
					if !ok {
						break
					}
					writes[sel] = true
					e = sel.X
				}
			}
			return true
		})
		ast.Inspect(d, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && pkgs[id.Name] {
				return true
			}
			c := out[key]
			switch sel.Sel.Name {
			case "EffectiveLosses":
				c.effective++
			case "SawLosses", "sawLosses":
				c.seal++
			case "Losses":
				switch {
				case writes[sel]:
					c.write++
				case marked[fset.Position(sel.Sel.Pos()).Line]:
					c.raw++
				default:
					c.unlabelled++
				}
			default:
				return true
			}
			out[key] = c
			return true
		})
	}
	return out
}

// compareLossSites lists every difference between what the scan found and
// the table: an unlisted function, a changed count, an unlabelled read, a
// listed function that no longer touches losses.
func compareLossSites(got, want map[string]lossCounts) []string {
	var bad []string
	for k, g := range got {
		if g.unlabelled > 0 {
			bad = append(bad, fmt.Sprintf("%s: %d unlabelled .Losses read(s): call EffectiveLosses, or mark a deliberate raw read with %q trailing on its line or alone on the line before", k, g.unlabelled, rawMarker+"<why>"))
		}
		w, ok := want[k]
		switch {
		case !ok:
			bad = append(bad, fmt.Sprintf("%s: unlisted losses site %+v; add it to lossSites with its label", k, g))
		case g != w:
			bad = append(bad, fmt.Sprintf("%s: found %+v, table says %+v", k, g, w))
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			bad = append(bad, k+": listed in lossSites but no longer touches losses; drop the entry")
		}
	}
	slices.Sort(bad)
	return bad
}

// TestEveryLossesReadSiteIsLabelled walks every non-test Go file under cmd/wt
// and internal/ and checks each use of a row's Losses (and of an item's
// sealed SawLosses, and each EffectiveLosses call) against lossSites. Why: a
// rescued original's raw Losses still count the commits its record keeps; a
// consumer that reads them raw by mistake would refuse or mis-warn about a
// preserved original, and one that reads the view where raw is meant (rescue,
// the exit prompt, git's --force) would offer or force what it must not. A
// new read site fails here until its author labels it.
func TestEveryLossesReadSiteIsLabelled(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	got := map[string]lossCounts{}
	scanned := 0
	for _, dir := range []string{"cmd/wt", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" || (p != filepath.Join(root, dir) && strings.HasPrefix(d.Name(), ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, p, nil, parser.ParseComments|parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			scanned++
			for k, c := range scanLosses(fset, file, filepath.ToSlash(rel)) {
				got[k] = c
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned < 100 {
		t.Fatalf("scanned only %d files; the walk is not reaching the sources", scanned)
	}
	for _, b := range compareLossSites(got, lossSites) {
		t.Error(b)
	}
}

// TestLossesGuardBites feeds the scanner small sources and checks each kind
// of mistake is found: an unmarked read, a marker with no reason, a new
// function, a changed count, a stale entry, a standalone marker two lines
// above the read, a trailing marker on the line before it; and that the
// legitimate shapes (a standalone marker on the line before, a trailing one
// on the read's own line, a write, the view, the seal, a package-qualified
// type) count as such. Why: a guard that silently matches nothing would pass
// on any change.
func TestLossesGuardBites(t *testing.T) {
	src := `package p

import "x/cleanup"

func unmarked(r cleanup.Row) bool { return r.Losses.Dirty }

func noWhy(r cleanup.Row) bool {
	// losses: raw,
	return r.Losses.Dirty
}

func marked(r cleanup.Row) bool {
	// losses: raw, git needs the real value
	return r.Losses.Dirty
}

func sameLine(r cleanup.Row) bool {
	return r.Losses.Dirty // losses: raw, same line
}

func view(r cleanup.Row, it Item) bool {
	return r.EffectiveLosses().Dirty || it.SawLosses.Dirty || r.Losses == (cleanup.Losses{}) // losses: raw, compared whole
}

func build(r *cleanup.Row) { r.Losses.Dirty = true; r.Losses.Unpushed++ }

func standaloneTooFar(r cleanup.Row) bool {
	// losses: raw, a standalone marker covers only the next line
	x := 1
	return r.Losses.Dirty && x > 0
}

func trailingNextLine(r cleanup.Row) bool {
	x := 1 // losses: raw, a trailing marker covers only its own line
	return r.Losses.Dirty && x > 0
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	got := scanLosses(fset, file, "p.go")
	want := map[string]lossCounts{
		"p.go#unmarked": {unlabelled: 1},
		"p.go#noWhy":    {unlabelled: 1},
		"p.go#marked":   {raw: 1},
		"p.go#sameLine": {raw: 1},
		"p.go#view":     {raw: 1, effective: 1, seal: 1},
		"p.go#build":    {write: 2},
		// Neither marker reaches the read: a standalone one two lines above,
		// a trailing one on the line before.
		"p.go#standaloneTooFar": {unlabelled: 1},
		"p.go#trailingNextLine": {unlabelled: 1},
	}
	if len(got) != len(want) {
		t.Errorf("scan = %+v, want %+v", got, want)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %+v, want %+v", k, got[k], w)
		}
	}

	table := map[string]lossCounts{
		"p.go#marked":   {raw: 1},
		"p.go#sameLine": {raw: 2}, // a changed count
		"p.go#view":     {raw: 1, effective: 1, seal: 1},
		"p.go#build":    {write: 2},
		"p.go#gone":     {effective: 1}, // stale
	}
	bad := compareLossSites(got, table)
	for _, frag := range []string{
		"p.go#unmarked: 1 unlabelled",
		"p.go#unmarked: unlisted",
		"p.go#noWhy: 1 unlabelled",
		"p.go#sameLine: found",
		"p.go#gone: listed in lossSites but no longer",
		"p.go#standaloneTooFar: 1 unlabelled",
		"p.go#trailingNextLine: 1 unlabelled",
	} {
		if !slices.ContainsFunc(bad, func(b string) bool { return strings.HasPrefix(b, frag) }) {
			t.Errorf("no finding %q in %q", frag, bad)
		}
	}
	if len(bad) != 10 { // the seven above plus noWhy, standaloneTooFar and trailingNextLine unlisted
		t.Errorf("%d findings, want 10: %q", len(bad), bad)
	}
	if b := compareLossSites(got, want); len(b) != 4 { // only the four unlabelled reads
		t.Errorf("findings against an exact table = %q, want the four unlabelled reads", b)
	}
}
