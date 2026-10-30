// Package slotedit edits slot-profiles.toml as text. The file is written by
// hand and annotated with comments, so an edit changes only the lines it
// targets and keeps every other byte: Add appends an entry, Set rewrites,
// inserts or deletes key lines inside one entry, and Remove deletes one
// entry's lines with the comment block directly above it.
//
// The editor is line-based. An entry starts at a line whose text is
// [[profile]] (spaces and a trailing comment allowed). Its comment block is
// the contiguous run of # lines directly above that line; a blank line ends
// the run, so a file header followed by a blank line belongs to no entry.
// An entry's block runs from its comment block to the line before the next
// entry's comment block, or to the end of the file. Its content ends at its
// last line that is neither blank nor a comment: a comment paragraph after
// that (a divider, notes, a commented-out entry) belongs to no entry, and
// Remove and Set leave it where it is. A comment inside the entry, directly
// above one of its keys or tables, is part of the entry.
//
// The editor does not follow multi-line strings, so it refuses a file that
// has one: a triple-quote delimiter, double or single, outside a comment.
//
// The package checks the text it edits, not the profile it describes:
// whether the result is a valid profile is the caller's question
// (profiles.ParseSlots and ValidateSlots).
package slotedit

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// Patch is a change to one entry. A nil or empty field changes nothing.
type Patch struct {
	Slots    map[string]string // slot → registry id; set or added
	Default  *string           // the new default
	SetEnv   map[string]string // env key → value; set or added
	UnsetEnv []string          // env keys to remove; each must be set
}

// slotOrder is the order Add writes, and Set inserts, Claude Code's slots;
// any other slot follows, sorted.
var slotOrder = []string{"fable", "opus", "sonnet", "haiku"}

// line is one line of the file, with its "\n" (the last line may lack it).
type line = string

// entryBlock is one [[profile]] entry as line indices into the file.
type entryBlock struct {
	first  int // the first line of its comment block (== header when none)
	header int // the [[profile]] line
	end    int // one past its last line, before the next entry's comment block
	// contentEnd is one past its last line that is neither blank nor a
	// comment. What follows it up to end belongs to no entry.
	contentEnd int
	name       string
}

// splitLines splits src into lines that keep their "\n".
func splitLines(src []byte) []line {
	lines := strings.SplitAfter(string(src), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// errMultiline is scan's refusal of a file with a multi-line string.
var errMultiline = errors.New(`slotedit: the file has a multi-line string (""" or '''), which this editor does not follow; edit it by hand`)

// scan is the one block scanner Add, Set, Remove and LeadingComments share.
// It returns errMultiline, with the lines and entries it found anyway, for a
// file that has a multi-line string delimiter outside a comment.
func scan(src []byte) ([]line, []entryBlock, error) {
	lines := splitLines(src)
	var heads []int
	var err error
	for i, l := range lines {
		if !isComment(l) && (strings.Contains(l, `"""`) || strings.Contains(l, "'''")) {
			err = errMultiline
		}
		if isProfileHeader(l) {
			heads = append(heads, i)
		}
	}
	entries := make([]entryBlock, len(heads))
	for k, h := range heads {
		lower := 0
		if k > 0 {
			lower = heads[k-1] + 1
		}
		first := h
		for first > lower && isComment(lines[first-1]) {
			first--
		}
		entries[k] = entryBlock{first: first, header: h}
	}
	for k := range entries {
		entries[k].end = len(lines)
		if k+1 < len(entries) {
			entries[k].end = entries[k+1].first
		}
		entries[k].contentEnd = entries[k].header + 1
		for i := entries[k].end - 1; i > entries[k].header; i-- {
			if !isBlank(lines[i]) && !isComment(lines[i]) {
				entries[k].contentEnd = i + 1
				break
			}
		}
		entries[k].name = entryName(lines, entries[k])
	}
	return lines, entries, err
}

// entryName is the value of the entry's first name = "..." line before its
// first table header, or "" when it has none.
func entryName(lines []line, e entryBlock) string {
	for i := e.header + 1; i < e.end; i++ {
		if _, ok := tableName(lines[i]); ok {
			return ""
		}
		kv, ok := parseKV(lines[i])
		if !ok || kv.key != "name" || kv.valEnd < 0 {
			continue
		}
		var v struct{ V string }
		if _, err := toml.Decode("V = "+lines[i][kv.valStart:kv.valEnd], &v); err != nil {
			return ""
		}
		return v.V
	}
	return ""
}

func isBlank(l line) bool   { return strings.TrimSpace(l) == "" }
func isComment(l line) bool { return strings.HasPrefix(strings.TrimSpace(l), "#") }

// tableName reports whether l is a table or array-of-tables header, and its
// name with the spaces around dots removed ("profile.slots").
func tableName(l line) (string, bool) {
	s := strings.TrimSpace(l)
	open, closing := "[", "]"
	if strings.HasPrefix(s, "[[") {
		open, closing = "[[", "]]"
	} else if !strings.HasPrefix(s, "[") {
		return "", false
	}
	idx := strings.Index(s, closing)
	if idx < 0 {
		return "", false
	}
	rest := strings.TrimSpace(s[idx+len(closing):])
	if rest != "" && rest[0] != '#' {
		return "", false
	}
	parts := strings.Split(s[len(open):idx], ".")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return open + strings.Join(parts, ".") + closing, true
}

func isProfileHeader(l line) bool {
	n, ok := tableName(l)
	return ok && n == "[[profile]]"
}

// kvLine is a parsed `key = value` line. valEnd is -1 when the value is not
// a one-line string, which the editor does not rewrite.
type kvLine struct {
	key              string
	valStart, valEnd int
}

// parseKV parses a one-key line. A dotted key, a comment, a header or a
// blank line is not one.
func parseKV(l line) (kvLine, bool) {
	i := len(l) - len(strings.TrimLeft(l, " \t"))
	if i >= len(l) {
		return kvLine{}, false
	}
	var key string
	switch c := l[i]; {
	case c == '"' || c == '\'':
		end := scanString(l, i)
		if end < 0 {
			return kvLine{}, false
		}
		var v struct{ V string }
		if _, err := toml.Decode("V = "+l[i:end], &v); err != nil {
			return kvLine{}, false
		}
		key, i = v.V, end
	case isBareChar(c):
		j := i
		for j < len(l) && isBareChar(l[j]) {
			j++
		}
		key, i = l[i:j], j
	default:
		return kvLine{}, false
	}
	for i < len(l) && (l[i] == ' ' || l[i] == '\t') {
		i++
	}
	if i >= len(l) || l[i] != '=' {
		return kvLine{}, false
	}
	i++
	for i < len(l) && (l[i] == ' ' || l[i] == '\t') {
		i++
	}
	kv := kvLine{key: key, valStart: i, valEnd: -1}
	if i < len(l) && (l[i] == '"' || l[i] == '\'') {
		kv.valEnd = scanString(l, i)
	}
	return kv, true
}

// scanString returns one past the closing quote of the one-line string that
// opens at l[i], or -1 for a multi-line or unterminated string.
func scanString(l line, i int) int {
	q := l[i]
	if strings.HasPrefix(l[i:], strings.Repeat(string(q), 3)) {
		return -1
	}
	for j := i + 1; j < len(l); j++ {
		switch {
		case l[j] == '\n':
			return -1
		case q == '"' && l[j] == '\\':
			j++
		case l[j] == q:
			return j + 1
		}
	}
	return -1
}

// dottedRoot returns the first part of a dotted bare key (`env.K = "v"`
// gives "env").
func dottedRoot(l line) (string, bool) {
	s := strings.TrimLeft(l, " \t")
	j := 0
	for j < len(s) && isBareChar(s[j]) {
		j++
	}
	if j == 0 {
		return "", false
	}
	rest := strings.TrimLeft(s[j:], " \t")
	return s[:j], strings.HasPrefix(rest, ".")
}

func isBareChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}

var (
	formatKey    = tomlw.FormatKey
	formatString = tomlw.FormatString
)

// keyLine writes `key = "value"\n` with the key padded to width columns.
func keyLine(key, value string, width int) string {
	k := formatKey(key)
	if pad := width - len(k); pad > 0 {
		k += strings.Repeat(" ", pad)
	}
	return k + " = " + formatString(value) + "\n"
}

// orderedSlots lists m's keys in slotOrder, then the rest sorted.
func orderedSlots(m map[string]string) []string {
	var out, rest []string
	for _, s := range slotOrder {
		if _, ok := m[s]; ok {
			out = append(out, s)
		}
	}
	for s := range m {
		if !slices.Contains(slotOrder, s) {
			rest = append(rest, s)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// find returns the one entry named name.
func find(entries []entryBlock, name string) (entryBlock, error) {
	if name == "" {
		return entryBlock{}, errors.New("slotedit: empty profile name")
	}
	var hits []entryBlock
	for _, e := range entries {
		if e.name == name {
			hits = append(hits, e)
		}
	}
	switch len(hits) {
	case 0:
		return entryBlock{}, fmt.Errorf("slotedit: no profile named %q", name)
	case 1:
		return hits[0], nil
	default:
		return entryBlock{}, fmt.Errorf("slotedit: %d profiles are named %q; edit the file by hand", len(hits), name)
	}
}

// LeadingComments maps each named entry to its comment block, each line
// without its "#" or "# " prefix, joined by "\n"; "" when it has none. A
// name used twice keeps its first entry's comment.
func LeadingComments(src []byte) map[string]string {
	lines, entries, _ := scan(src) // best effort: a comment map is read-only
	out := map[string]string{}
	for _, e := range entries {
		if e.name == "" {
			continue
		}
		if _, seen := out[e.name]; seen {
			continue
		}
		var parts []string
		for _, l := range lines[e.first:e.header] {
			s := strings.TrimRight(strings.TrimLeft(l, " \t"), "\n")
			s = strings.TrimPrefix(s, "#")
			s = strings.TrimPrefix(s, " ")
			parts = append(parts, s)
		}
		out[e.name] = strings.Join(parts, "\n")
	}
	return out
}

// Add appends p as a new entry, preceded by comment (one "# " line per
// line of it; "" writes none). Every byte of src is kept; one blank line
// separates the new entry from what is above it, and the result ends in one
// newline. It refuses a name an entry already has, and a profile with args,
// config_content or wrapper, which it does not write.
func Add(src []byte, p profiles.SlotProfile, comment string) ([]byte, error) {
	if p.Name == "" {
		return nil, errors.New("slotedit: empty profile name")
	}
	if len(p.Args) > 0 || len(p.ConfigContent) > 0 || p.Wrapper != nil {
		return nil, fmt.Errorf("slotedit: profile %q has args, config_content or wrapper, which add does not write; edit the file by hand", p.Name)
	}
	lines, entries, err := scan(src)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.name == p.Name {
			return nil, fmt.Errorf("slotedit: a profile named %q already exists", p.Name)
		}
	}

	var b strings.Builder
	b.Write(src)
	if len(src) > 0 {
		if !bytes.HasSuffix(src, []byte("\n")) {
			b.WriteString("\n")
		}
		if !isBlank(lines[len(lines)-1]) {
			b.WriteString("\n")
		}
	}
	if c := strings.TrimRight(comment, "\n"); c != "" {
		for _, l := range strings.Split(c, "\n") {
			if l == "" {
				b.WriteString("#\n")
			} else {
				b.WriteString("# " + l + "\n")
			}
		}
	}
	b.WriteString("[[profile]]\n")
	b.WriteString(keyLine("name", p.Name, 7))
	b.WriteString(keyLine("agent", p.Agent, 7))
	b.WriteString(keyLine("default", p.Default, 7))
	b.WriteString("\n[profile.slots]\n")
	slots := orderedSlots(p.Slots)
	width := 0
	for _, s := range slots {
		width = max(width, len(formatKey(s)))
	}
	for _, s := range slots {
		b.WriteString(keyLine(s, p.Slots[s], width))
	}
	if len(p.Env) > 0 {
		b.WriteString("\n[profile.env]\n")
		for _, k := range sortedKeys(p.Env) {
			b.WriteString(keyLine(k, p.Env[k], 0))
		}
	}
	return []byte(b.String()), nil
}

// section is one table of an entry: "" for the entry's own keys, else the
// header's name ("[profile.slots]").
type section struct {
	head int // the header line (the [[profile]] line for "")
	keys map[string]int
	last int // the last key line, or head when there is none
	eqs  []int
}

// sections parses the tables of e. A table that appears twice is refused.
func sections(lines []line, e entryBlock) (map[string]*section, error) {
	out := map[string]*section{}
	cur := &section{head: e.header, keys: map[string]int{}, last: e.header}
	out[""] = cur
	for i := e.header + 1; i < e.end; i++ {
		if n, ok := tableName(lines[i]); ok {
			if _, dup := out[n]; dup {
				return nil, fmt.Errorf("slotedit: profile %q has %s twice", e.name, n)
			}
			cur = &section{head: i, keys: map[string]int{}, last: i}
			out[n] = cur
			continue
		}
		if cur == out[""] {
			if root, ok := dottedRoot(lines[i]); ok && (root == "slots" || root == "env") {
				return nil, fmt.Errorf("slotedit: profile %q writes %s as dotted keys; edit the file by hand", e.name, root)
			}
		}
		if kv, ok := parseKV(lines[i]); ok {
			cur.keys[kv.key] = i
			cur.last = i
			cur.eqs = append(cur.eqs, strings.Index(lines[i], "="))
		}
	}
	return out, nil
}

// width is the column an inserted key pads to: the '=' column every key of
// the table shares, when the table aligns its keys; else 0 (`k = v`).
func (s *section) width() int {
	if len(s.eqs) == 0 {
		return 0
	}
	for _, c := range s.eqs[1:] {
		if c != s.eqs[0] {
			return 0
		}
	}
	return s.eqs[0] - 1
}

// Set applies patch to the one entry named name, changing only the lines it
// targets: a key that exists has its value replaced in place (padding and a
// trailing comment kept), a missing key is inserted after its table's last
// key, a missing table is added after the entry's last key or table (before
// any blank lines and comment paragraph that follow it), and an unset env
// key loses its line. It refuses a name that
// matches no entry or more than one.
func Set(src []byte, name string, patch Patch) ([]byte, error) {
	lines, entries, err := scan(src)
	if err != nil {
		return nil, err
	}
	e, err := find(entries, name)
	if err != nil {
		return nil, err
	}
	secs, err := sections(lines, e)
	if err != nil {
		return nil, err
	}
	for _, inline := range []string{"slots", "env"} {
		if _, ok := secs[""].keys[inline]; ok {
			return nil, fmt.Errorf("slotedit: profile %q writes %s as an inline table; edit the file by hand", name, inline)
		}
	}
	for _, k := range patch.UnsetEnv {
		if _, ok := patch.SetEnv[k]; ok {
			return nil, fmt.Errorf("slotedit: env key %q is both set and unset", k)
		}
	}
	for s, v := range patch.Slots {
		if v == "" {
			return nil, fmt.Errorf("slotedit: slot %q: empty model id", s)
		}
	}

	replace := map[int]string{}
	drop := map[int]bool{}
	insert := map[int][]string{}

	// put sets key in the table named tbl, or returns the line for a table
	// that does not exist yet.
	put := func(tbl, key, value string) (string, error) {
		sec, ok := secs[tbl]
		if !ok {
			return keyLine(key, value, 0), nil
		}
		if i, ok := sec.keys[key]; ok {
			kv, _ := parseKV(lines[i])
			if kv.valEnd < 0 {
				return "", fmt.Errorf("slotedit: profile %q: %s is not a one-line string; edit the file by hand", name, key)
			}
			replace[i] = lines[i][:kv.valStart] + formatString(value) + lines[i][kv.valEnd:]
			return "", nil
		}
		insert[sec.last] = append(insert[sec.last], keyLine(key, value, sec.width()))
		return "", nil
	}
	// table adds the new-table text for tbl, when put returned lines for it.
	var newTables []string
	table := func(tbl string, rows []string) {
		if len(rows) > 0 {
			newTables = append(newTables, "\n"+tbl+"\n"+strings.Join(rows, ""))
		}
	}

	if patch.Default != nil {
		if _, err := put("", "default", *patch.Default); err != nil {
			return nil, err
		}
	}
	var rows []string
	for _, s := range orderedSlots(patch.Slots) {
		r, err := put("[profile.slots]", s, patch.Slots[s])
		if err != nil {
			return nil, err
		}
		if r != "" {
			rows = append(rows, r)
		}
	}
	table("[profile.slots]", rows)
	rows = nil
	for _, k := range sortedKeys(patch.SetEnv) {
		r, err := put("[profile.env]", k, patch.SetEnv[k])
		if err != nil {
			return nil, err
		}
		if r != "" {
			rows = append(rows, r)
		}
	}
	table("[profile.env]", rows)
	for _, k := range patch.UnsetEnv {
		sec, ok := secs["[profile.env]"]
		i, has := 0, false
		if ok {
			i, has = sec.keys[k]
		}
		if !has {
			return nil, fmt.Errorf("slotedit: profile %q does not set env key %q", name, k)
		}
		drop[i] = true
	}
	if len(newTables) > 0 {
		at := e.contentEnd - 1
		insert[at] = append(insert[at], newTables...)
	}

	var b strings.Builder
	for i, l := range lines {
		if r, ok := replace[i]; ok {
			l = r
		}
		if !drop[i] {
			b.WriteString(l)
		}
		if add := insert[i]; len(add) > 0 {
			if !drop[i] && !strings.HasSuffix(l, "\n") {
				b.WriteString("\n")
			}
			b.WriteString(strings.Join(add, ""))
		}
	}
	return []byte(b.String()), nil
}

// Remove deletes the one entry named name: the comment block directly above
// it, its lines up to its content's end, and the blank lines right after
// them. A comment paragraph after the entry's content belongs to no entry
// and stays. When nothing follows, the blank lines left at the end of the
// file go too, so the file ends in one newline. It refuses a
// name that matches no entry or more than one.
func Remove(src []byte, name string) ([]byte, error) {
	lines, entries, err := scan(src)
	if err != nil {
		return nil, err
	}
	e, err := find(entries, name)
	if err != nil {
		return nil, err
	}
	cut := e.contentEnd
	for cut < len(lines) && isBlank(lines[cut]) {
		cut++
	}
	kept := append(slices.Clone(lines[:e.first]), lines[cut:]...)
	if cut == len(lines) {
		for len(kept) > 0 && isBlank(kept[len(kept)-1]) {
			kept = kept[:len(kept)-1]
		}
	}
	return []byte(strings.Join(kept, "")), nil
}
