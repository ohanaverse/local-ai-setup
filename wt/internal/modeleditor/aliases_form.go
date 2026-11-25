package modeleditor

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/editorkit"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// The alias form's fields, top to bottom.
const (
	afID = iota
	afTargets
	afCount
)

var aliasFormLabels = [afCount]string{"Alias", "Targets"}

// aliasForm is the add / edit form of the Aliases tab, hand-built on
// bubbles/textinput like the model form: the alias id (an add only; "alias/"
// is supplied when left out) and one Targets field in the command's own
// syntax, "model[:order], model[:order]".
type aliasForm struct {
	edit   bool
	id     string // the alias being edited
	text   [afCount]textinput.Model
	cursor int
	err    string
	saving bool
}

// editable is false for the id of an edit.
func (f *aliasForm) editable(field int) bool { return field != afID || !f.edit }

// value is field's text as shown.
func (f *aliasForm) value(field int) string {
	if field == afID && f.edit {
		return f.id
	}
	return f.text[field].Value()
}

// normalizeAliasID is an id as typed with the "alias/" prefix supplied.
func normalizeAliasID(s string) string {
	s = strings.TrimSpace(s)
	if s != "" && !strings.HasPrefix(s, config.AliasIDPrefix) {
		s = config.AliasIDPrefix + s
	}
	return s
}

// openAliasForm opens the form for a new alias (v nil) or v's targets.
func (m *model) openAliasForm(v *modeladmin.AliasView) {
	al := &m.aliases
	f := &aliasForm{}
	idText, targets := "", ""
	if v != nil {
		f.edit, f.id = true, v.ID
		if a, ok := al.cfg.AliasByID(v.ID); ok {
			targets = modeladmin.AliasTargetsText(al.cfg, a)
		}
	}
	f.text[afID] = editorkit.NewTextInput(idText, "alias/name")
	f.text[afTargets] = editorkit.NewTextInput(targets, "model[:order], model[:order]")
	for i := range f.text {
		f.text[i].Prompt = ""
	}
	f.cursor = afID
	if f.edit {
		f.cursor = afTargets
	}
	al.form, al.phase = f, aliasesForm
	m.focusAliasField()
	m.resizeAliasForm()
}

func (m *model) focusAliasField() {
	f := m.aliases.form
	for i := range f.text {
		f.text[i].Blur()
	}
	f.text[f.cursor].Focus()
	f.text[f.cursor].CursorEnd()
}

// aliasFieldRoom is how many columns a field's value has beside its label.
func (m *model) aliasFieldRoom(field int) int {
	return max(m.width-utf8.RuneCountInString(aliasFormLabels[field])-4, 4)
}

func (m *model) resizeAliasForm() {
	f := m.aliases.form
	if f == nil {
		return
	}
	for i := range f.text {
		f.text[i].Width = max(m.aliasFieldRoom(i)-1, 3)
	}
}

func (m *model) moveAliasField(step int) {
	f := m.aliases.form
	for {
		f.cursor = (f.cursor + step + afCount) % afCount
		if f.editable(f.cursor) {
			break
		}
	}
	m.focusAliasField()
}

// aliasFormHints are the form's key hints, fullest first.
var aliasFormHints = []string{
	"ctrl+s save · tab next field · esc cancel",
	"ctrl+s · tab · esc",
}

const aliasFormErrorHint = "press a key to go back to the form"

// aliasFormRender draws the form and says whether the error is so long that
// it has the screen to itself (the fields would otherwise not fit): the next
// key then only brings the fields back.
func (m *model) aliasFormRender() (view string, errAlone bool) {
	f := m.aliases.form
	width := max(m.width, 1)
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	title := "Add an alias"
	if f.edit {
		title = "Edit " + visible(f.id)
	}
	switch {
	case f.saving && m.quitPending:
		title += "  (saving, then quitting...)"
	case f.saving:
		title += "  (saving...)"
	}
	var fields []editorkit.FormField
	for i := 0; i < afCount; i++ {
		value := f.value(i)
		switch {
		case !f.editable(i):
			value = middleCut(visible(value), m.aliasFieldRoom(i))
		case i == f.cursor:
			value = f.text[i].View()
		default:
			value = endCut(value, m.aliasFieldRoom(i))
		}
		fields = append(fields, editorkit.FormField{Label: aliasFormLabels[i], Value: value, Focus: i == f.cursor})
	}
	rows := tuilayout.Clip(strings.TrimRight(editorkit.RenderFormFields(m.theme, fields), "\n"), width)
	hints := dim.Render(editorkit.FitHints(m.width, aliasFormHints))
	head := tabBar(m.theme, TabAliases) + "\n" + editorkit.WrapText(title, width) + "\n"
	errBlock := ""
	if f.err != "" {
		errBlock = themes.ErrorStyle(m.theme).Render(editorkit.WrapText(f.err, width)) + "\n"
	}
	build := func(gap, help string) string {
		return head + gap + rows + "\n" + help + errBlock + gap + hints
	}
	help := dim.Render(tuilayout.Clip("targets: model[:order], model[:order]; lowest order is tried first", width)) + "\n"
	for _, v := range []string{build("\n", help), build("", help), build("", "")} {
		if m.height <= 0 || lipgloss.Height(v) <= m.height {
			return v, false
		}
	}
	if f.err != "" {
		return tuilayout.MessageAlone(tabBar(m.theme, TabAliases), editorkit.WrapText(f.err, width), aliasFormErrorHint, width, m.height, dim), true
	}
	return build("", ""), false
}

func (m *model) aliasFormView() string {
	v, _ := m.aliasFormRender()
	return v
}

// updateAliasForm handles a message while the form is showing.
func (m *model) updateAliasForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	f := m.aliases.form
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if f.saving {
		if k.Type == tea.KeyCtrlC {
			return m.quit()
		}
		return m, nil
	}
	if _, alone := m.aliasFormRender(); alone && k.Type != tea.KeyCtrlC {
		f.err = ""
		return m, nil
	}
	switch k.Type {
	case tea.KeyCtrlC:
		return m.quit()
	case tea.KeyEsc:
		m.aliases.phase, m.aliases.form = aliasesList, nil
		return m, nil
	case tea.KeyCtrlS:
		return m, m.saveAliasForm()
	case tea.KeyEnter:
		if f.cursor == afTargets {
			return m, m.saveAliasForm()
		}
		m.moveAliasField(1)
		return m, nil
	case tea.KeyTab, tea.KeyDown:
		m.moveAliasField(1)
		return m, nil
	case tea.KeyShiftTab, tea.KeyUp:
		m.moveAliasField(-1)
		return m, nil
	}
	var cmd tea.Cmd
	f.text[f.cursor], cmd = f.text[f.cursor].Update(k)
	return m, cmd
}

// sameTargets is true when two target lists are one list: the same models in
// the same order, an order the file leaves out read as its position.
func sameTargets(a, b []config.AliasTarget) bool {
	norm := func(ts []config.AliasTarget) []config.AliasTarget {
		out := slices.Clone(ts)
		for i := range out {
			if out[i].Order < 1 {
				out[i].Order = i + 1
			}
		}
		return out
	}
	return slices.Equal(norm(a), norm(b))
}

// formTargetError is a target parser's refusal worded for a field, not a
// flag: ParseAliasTargets is the command's, and says "--target".
func formTargetError(err error) string {
	return strings.ReplaceAll(err.Error(), "--target ", "target ")
}

// saveAliasForm checks the form with the real checks and starts the write, or
// shows why not, or asks the shrink question. Nothing here reads a file: the
// checks run over the config the tab last loaded, and the write checks again
// under the registry lock.
func (m *model) saveAliasForm() tea.Cmd {
	al := &m.aliases
	f := al.form
	fail := func(field int, msg string) tea.Cmd {
		f.err = msg
		if f.editable(field) {
			f.cursor = field
			m.focusAliasField()
		}
		return nil
	}
	id := f.id
	if !f.edit {
		id = normalizeAliasID(f.text[afID].Value())
		if id == "" {
			return fail(afID, "the alias needs an id: alias/<name>")
		}
		if _, exists := al.cfg.AliasByID(id); exists {
			return fail(afID, id+" is already an alias: select it in the list and press enter to edit it")
		}
	}
	targets, err := modeladmin.ParseAliasTargetsText(al.cfg, f.text[afTargets].Value())
	if err != nil {
		return fail(afTargets, formTargetError(err))
	}
	req := modeladmin.AliasRequest{ID: id, Targets: targets}
	if err := modeladmin.CheckAlias(al.cfg, req); err != nil {
		field := afTargets
		if strings.Contains(err.Error(), "is not \"alias/<name>\"") {
			field = afID
		}
		return fail(field, err.Error())
	}
	if cur, ok := al.cfg.AliasByID(id); ok && sameTargets(al.cfg.AliasTargets(cur), targets) {
		al.phase, al.form = aliasesList, nil
		al.status, al.selectID = "no change: "+id, id
		return nil
	}
	wc := modeladmin.AliasWindowChange(al.cfg, al.ix, req)
	if wc.Shrinks {
		al.pending, al.wc, al.phase = req, wc, aliasesConfirm
		return nil
	}
	return m.startAliasWrite(req)
}
