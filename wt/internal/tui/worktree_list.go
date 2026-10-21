package tui

import (
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// entryKind discriminates picker rows: real entries, the create-new
// sentinel, and the separator that visually divides locals from remotes.
type entryKind int

const (
	// kindEntry is a real worktree or branch row.
	kindEntry entryKind = iota
	// kindNewWorktree is the "+ New worktree…" sentinel that opens
	// the new-worktree prompt. The underlying entry field is unused.
	kindNewWorktree
	// kindSeparator is a non-selectable visual divider rendered
	// between the locals and remotes groups.
	kindSeparator
)

// entryItem adapts a worktree.Entry to a list.Item, or represents a
// sentinel/separator row. label carries the rendered text for
// separator rows and "(current)"/"(default)" markers for entry rows;
// the underlying entry worktree.Entry is never mutated, so it stays
// safe to forward into selectedEntryMsg at launch time.
//
// prBranch is the head branch name the row's pull request is looked up under
// (worktree.PRBranch for a worktree or local row; a remote-tracking row's
// Entry.RemoteBranch, its name without the remote, "" when the remote is
// unknown), set by buildList, which alone knows the row's group; repo is a
// remote-tracking row's Entry.RemoteRepo, the owner/repo of its remote: the
// row is tagged only with a PR whose head is that repository and prBranch
// (#388, #387).
// pr is the pull request found, set by applyPRs once `gh pr list` has
// answered, nil until then and for a branch with none. remote marks a
// remote-tracking row, the only kind whose displayed name drops its first
// path segment (the remote).
type entryItem struct {
	kind     entryKind
	entry    worktree.Entry
	label    string
	prBranch string
	repo     string
	pr       *worktree.PR
	remote   bool
}

// FilterValue is used by the list's built-in filter. Non-entry rows
// (sentinel and separator) return "" so they are hidden when the user
// is filtering. A row with a pull request adds its number ("#383"), so
// typing the number finds the row.
func (e entryItem) FilterValue() string {
	if e.kind != kindEntry {
		return ""
	}
	if e.pr != nil {
		return e.entry.Branch + " " + prNumber(*e.pr)
	}
	return e.entry.Branch
}

// Title renders the branch name (a remote row's remote stripped for display)
// for real entries, with any (current)/(default) marker appended and then
// the pull request tag ("#383 open"); "+ New worktree…" for the sentinel;
// and the separator's label otherwise. The picker's delegate draws the same
// text with the tag coloured by state (prTagDelegate).
func (e entryItem) Title() string {
	if tag := e.prTag(); tag != "" {
		return e.baseTitle() + prTagGap + tag
	}
	return e.baseTitle()
}

// prTagGap separates the branch (and marker) from the pull request tag.
const prTagGap = "  "

// baseTitle is Title without the pull request tag.
func (e entryItem) baseTitle() string {
	switch e.kind {
	case kindNewWorktree:
		return "+ New worktree…"
	case kindSeparator:
		return e.label
	}
	b := e.entry.Branch
	if e.remote {
		// origin/foo shows as foo (everything after the first slash); a local
		// fix/foo keeps its whole name. The PR lookup uses entry.RemoteBranch
		// (split by the remote's real name), which differs from this display
		// name for a remote whose own name holds a slash.
		b = worktree.PRBranch(worktree.GroupRemoteBranches, e.entry)
	}
	if e.label != "" {
		b += "  " + e.label
	}
	return b
}

// prTag is the row's pull request tag, "#<number> <state>", or "" for a row
// with none. The state goes through printable: a state gh adds later is
// shown under its own name (worktree.PRState), text from outside wt.
func (e entryItem) prTag() string {
	if e.kind != kindEntry || e.pr == nil {
		return ""
	}
	return prNumber(*e.pr) + " " + printable(string(e.pr.State))
}

func prNumber(pr worktree.PR) string { return "#" + strconv.Itoa(pr.Number) }

// prTitleMax caps the pull request title in a row's description, so at 80
// columns some of the path still shows after it.
const prTitleMax = 48

// Description renders the metadata columns: the row's type, its pull
// request's title when it has one, and its path. The sentinel gets a static
// descriptor; separators get an empty descriptor.
func (e entryItem) Description() string {
	switch e.kind {
	case kindNewWorktree:
		return "create a new branch and worktree"
	case kindSeparator:
		return ""
	}
	path := e.entry.Path
	if path == "" {
		path = "(no worktree)"
	}
	parts := []string{fmt.Sprintf("%-9s", "["+string(e.entry.Type)+"]")}
	if e.pr != nil {
		if title := strings.TrimSpace(printable(e.pr.Title)); title != "" {
			parts = append(parts, ansi.Truncate(title, prTitleMax, "…")+" ·")
		}
	}
	parts = append(parts, path)
	return strings.TrimSpace(strings.Join(parts, " "))
}

// applyPRs tags every entry row of l with its pull request from prs (prFor,
// own the repository's remotes' repositories; a row with none loses any
// tag it had) and keeps what the list holds: the highlighted row (by
// identity, else by position), an applied filter and the query being typed.
// With a filter in place, the matches are recomputed here, in the same
// Update, against the new filter values, so the list is never drawn empty
// while they are recomputed and typing a PR number before the tags arrived
// finds the row once they do.
func applyPRs(l *list.Model, prs worktree.PRSet, own worktree.RepoSet) {
	selected, hadSelected := l.SelectedItem().(entryItem)
	index := l.Index()

	items := l.Items()
	next := make([]list.Item, len(items))
	for i, it := range items {
		if ei, ok := it.(entryItem); ok && ei.kind == kindEntry {
			ei.pr = prFor(ei, prs, own)
			it = ei
		}
		next[i] = it
	}
	if cmd := l.SetItems(next); cmd != nil {
		// SetItems returns the filter run as a command; running it now
		// keeps the visible rows (and the page the cursor is on) intact.
		if msg := cmd(); msg != nil {
			*l, _ = l.Update(msg)
		}
	}

	visible := l.VisibleItems()
	if hadSelected {
		for i, it := range visible {
			if ei, ok := it.(entryItem); ok && ei.kind == selected.kind && ei.entry == selected.entry && ei.label == selected.label {
				l.Select(i)
				return
			}
		}
	}
	if len(visible) > 0 {
		l.Select(min(index, len(visible)-1))
	}
}

// prFor is the pull request prs holds for ei, nil for none. A
// remote-tracking row has the PR whose head is its own remote's repository
// and branch (worktree.PRSet.ForRemote, compared without case): your fork's
// branch shows the PR you opened from it, upstream's branch upstream's own,
// and a remote whose repository is unknown is never guessed (#388, #387). A
// worktree or local row has no repository, so its branch name is looked up
// under the listed repository and own, the repository's remotes'
// repositories, and one PR chosen (worktree.PRSet.ForBranch: live over
// closed or merged, then the higher number); a stranger's fork PR never
// tags it. prsCurrent asks this with the same own, so the tags and the
// stale-filter check agree.
func prFor(ei entryItem, prs worktree.PRSet, own worktree.RepoSet) *worktree.PR {
	if ei.prBranch == "" {
		return nil
	}
	var pr worktree.PR
	var found bool
	if ei.remote {
		pr, found = prs.ForRemote(ei.repo, ei.prBranch)
	} else {
		pr, found = prs.ForBranch(ei.prBranch, own)
	}
	if !found {
		return nil
	}
	return &pr
}

// remoteTagsHidden is the notice for a listing that has pull requests from
// the listed repository's own branches (prs.BaseRepo) while no
// remote-tracking row sits in that repository or in any PR's head
// repository, so no remote row can be tagged (a renamed or transferred
// repository, a remote reached by another path form). A listing whose PRs
// all come from forks names no repository and says nothing. "" when nothing
// is hidden for that reason.
func remoteTagsHidden(items []list.Item, prs worktree.PRSet) string {
	if len(prs.ByHead) == 0 || prs.BaseRepo == "" {
		return ""
	}
	remotes := 0
	for _, it := range items {
		ei, ok := it.(entryItem)
		if !ok || ei.kind != kindEntry || !ei.remote {
			continue
		}
		remotes++
		if strings.EqualFold(ei.repo, prs.BaseRepo) || prs.HasHeadRepo(ei.repo) {
			return ""
		}
	}
	if remotes == 0 {
		return ""
	}
	return "PR tags hidden on remote branches: no remote is " + printable(prs.BaseRepo)
}

// prsCurrent reports whether every entry row in items carries the tag prs
// gives it. A filter run that a keystroke started before the latest tags
// arrived matched the items as they were then, and its rows fail this.
func prsCurrent(items []list.Item, prs worktree.PRSet, own worktree.RepoSet) bool {
	for _, it := range items {
		ei, ok := it.(entryItem)
		if !ok || ei.kind != kindEntry {
			continue
		}
		want := prFor(ei, prs, own)
		if (want == nil) != (ei.pr == nil) || (want != nil && *want != *ei.pr) {
			return false
		}
	}
	return true
}

// selectedEntryMsg is emitted when the user picks a worktree or branch.
type selectedEntryMsg struct{ entry worktree.Entry }

// buildList constructs a list.Model from worktree groups in the order
// the picker should render them: sentinel, then the repo-root ((current))
// entry pinned second, then the rest of the worktrees group, then the
// local-branches group, then — only when the remote-branches group is
// non-empty — a separator row, then the remote-branches group. Entries
// matching the repo default branch are rendered with "(default)"; the
// entry matching the launch directory (after resolving symlinks) is
// rendered with "(current)", which wins over "(default)" so the current
// worktree stays distinguishable from a separate worktree on the default
// branch. The markers are tracked on entryItem.label so the underlying
// worktree.Entry is never mutated and remains safe to forward into
// selectedEntryMsg.
//
// repoRoot is resolved with filepath.EvalSymlinks so symlinked paths
// (e.g. ~/.worktrees/foo -> .worktrees/foo) compare correctly against
// the entry's Path.
func buildList(groups []worktree.EntryGroup, defaultBranch, repoRoot string, theme themes.Theme, width, height int) list.Model {
	resolvedRepo, _ := filepath.EvalSymlinks(repoRoot)

	items := make([]list.Item, 0)
	items = append(items, entryItem{kind: kindNewWorktree})

	// The repo-root entry is the picker's starting selection (set in
	// app.go), so it must be immediately reachable after the sentinel
	// rather than buried at its alphabetical position within the
	// worktrees group. Held aside here and spliced in below.
	var currentItem *entryItem

	for _, g := range groups {
		// Render the locals→remotes divider only when at least one remote
		// entry survives default-branch filtering; otherwise a dangling
		// "── remote branches ──" row with nothing below it would show in
		// repos whose only remote-tracking ref is the default branch.
		if g.Kind == worktree.GroupRemoteBranches {
			hasVisibleRemote := false
			for _, e := range g.Entries {
				if !worktree.SkipInPicker(g.Kind, e, defaultBranch) {
					hasVisibleRemote = true
					break
				}
			}
			if hasVisibleRemote {
				items = append(items, entryItem{kind: kindSeparator, label: "── remote branches ──"})
			}
		}
		for _, e := range g.Entries {
			// The default branch must never be a linked worktree, so skip
			// bare default-branch rows — the picker must not offer them as
			// create-targets. The primary checkout on the default branch
			// still appears as (current). The helper applies the same rule
			// to local default branches and remote-tracking refs under any
			// remote (origin/main, upstream/main, ...), while keeping a local
			// branch whose name happens to end in "/main" (e.g. feature/main)
			// pickable because it is not in the remote-branches group.
			if worktree.SkipInPicker(g.Kind, e, defaultBranch) {
				continue
			}
			ei := entryItem{kind: kindEntry, entry: e, prBranch: worktree.PRBranch(g.Kind, e)}
			if g.Kind == worktree.GroupRemoteBranches {
				// A remote row is looked up under its branch as the remote
				// knows it, split off by the remote's real name (which may
				// hold a slash), and only in its own repository (prFor).
				ei.remote, ei.prBranch, ei.repo = true, e.RemoteBranch, e.RemoteRepo
			}
			// Mark default-branch entries with (default). This applies to
			// non-current worktrees on the default branch (a linked worktree
			// created outside wt); bare default-branch rows are skipped above.
			if e.Branch == defaultBranch {
				ei.label = "(default)"
			}
			// The entry matching the launch directory is (current). This
			// wins over (default) so the current worktree stays
			// distinguishable from a separate worktree on the default
			// branch.
			if e.Path != "" {
				resolved, _ := filepath.EvalSymlinks(e.Path)
				if resolved == resolvedRepo {
					ei.label = "(current)"
				}
			}
			if ei.label == "(current)" {
				currentItem = &ei
				continue
			}
			items = append(items, ei)
		}
	}

	if currentItem != nil {
		items = append([]list.Item{items[0], *currentItem}, items[1:]...)
	}

	l := list.New(items, prTagDelegate{DefaultDelegate: ThemedListDelegate(theme), theme: theme}, width, height)
	l.Title = "Pick a worktree or branch"
	l.SetShowStatusBar(true)
	// Advertise the 'n' shortcut in the footer help line so users know they
	// can open the new-worktree prompt directly instead of scrolling to the
	// "+ New worktree…" sentinel row. bubbles/list renders these bindings in
	// both the short help line and the full help view, and hides them while
	// the user is actively typing a filter query.
	newBinding := key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new worktree"))
	l.AdditionalShortHelpKeys = func() []key.Binding { return []key.Binding{newBinding} }
	l.AdditionalFullHelpKeys = func() []key.Binding { return []key.Binding{newBinding} }
	return l
}

// prTagDelegate is the worktree picker's delegate: ThemedListDelegate, with a
// row's pull request tag drawn after its title in the colour of the PR's
// state (prTagStyle). The default delegate cannot colour part of a title: it
// styles the whole title, and while filtering it underlines matched
// characters by their position in the plain text, which escape codes inside
// the title would throw off. So the delegate draws the row without its tag,
// its title cut short enough to leave room for the tag, and then puts the
// styled tag on the end of the title line. A row with no tag is drawn as the
// default delegate draws it, and so is a row in a list too narrow to keep
// prTagMinTitle columns of title beside the tag, without the tag.
type prTagDelegate struct {
	list.DefaultDelegate
	theme themes.Theme
}

// prTagMinTitle is the fewest columns of title the tag may leave.
const prTagMinTitle = 10

// untaggedItem is an entry row as the default delegate draws it under the
// tag: the title without the tag, already cut to the room the tag leaves.
type untaggedItem struct {
	title string
	entryItem
}

func (u untaggedItem) Title() string { return u.title }

// Render draws one row; see prTagDelegate.
func (d prTagDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	ei, ok := item.(entryItem)
	tag := ""
	if ok {
		tag = ei.prTag()
	}
	if tag == "" {
		d.DefaultDelegate.Render(w, m, index, item)
		return
	}
	s := &d.Styles
	room := m.Width() - s.NormalTitle.GetPaddingLeft() - s.NormalTitle.GetPaddingRight() -
		ansi.StringWidth(prTagGap) - ansi.StringWidth(tag)
	if room < prTagMinTitle {
		// Too narrow for both: the branch is what the row is for.
		d.DefaultDelegate.Render(w, m, index, untaggedItem{title: ei.baseTitle(), entryItem: ei})
		return
	}
	var buf strings.Builder
	d.DefaultDelegate.Render(&buf, m, index, untaggedItem{title: ansi.Truncate(ei.baseTitle(), room, "…"), entryItem: ei})
	row := buf.String()
	titleLine, rest, hasRest := strings.Cut(row, "\n")

	style := prTagStyle(d.theme, ei.pr.State)
	emptyFilter := m.FilterState() == list.Filtering && m.FilterValue() == ""
	switch {
	case emptyFilter:
		style = s.DimmedTitle
	case index == m.Index() && m.FilterState() != list.Filtering:
		style = style.Bold(true)
	}
	_, _ = io.WriteString(w, titleLine+prTagGap+style.Render(tag))
	if hasRest {
		_, _ = io.WriteString(w, "\n"+rest)
	}
}

// prTagStyle colours a pull request tag by state, from the theme's tokens:
// open is success, draft is dim, merged is the header colour and closed is
// the error colour — GitHub's green, grey, purple-ish and red, as near as a
// theme's fixed token set gets.
func prTagStyle(theme themes.Theme, state worktree.PRState) lipgloss.Style {
	token := themes.TokenDim
	switch state {
	case worktree.PROpen:
		token = themes.TokenSuccess
	case worktree.PRMerged:
		token = themes.TokenHeader
	case worktree.PRClosed:
		token = themes.TokenError
	}
	return lipgloss.NewStyle().Foreground(theme.Token(token))
}

// printable drops the control characters (escape sequences' ESC included)
// from text that came from outside wt, such as a pull request title, so it
// cannot write to the terminal as anything but text. It drops the bidi
// controls too (unicode.Bidi_Control: the embeddings, overrides and
// isolates): an override left open in a title would reorder the rest of the
// description line, the path after it, in a terminal that applies bidi. A
// tab, newline or carriage return becomes a space, so the words either side
// of it stay apart.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return -1
		}
		return r
	}, s)
}
