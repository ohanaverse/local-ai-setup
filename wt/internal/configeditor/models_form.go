package configeditor

import (
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// The model form's fields, top to bottom.
const (
	mfProvider = iota
	mfName
	mfFamily
	mfTags
	mfLocation
	mfInput
	mfCache
	mfOutput
	mfSubPrice
	mfSubPeriod
	mfCount
)

// modelFormLabels are the fields' labels; modelFormFields the names
// modeladmin.FieldError uses for them, so a refused save puts the cursor on
// the field at fault.
var (
	modelFormLabels = [mfCount]string{"Provider", "Model name", "Family", "Tags", "Location", "Input $/M", "Cache $/M", "Output $/M", "Subscription $", "Subscription period"}
	modelFormFields = [mfCount]string{
		modeladmin.FieldProvider, modeladmin.FieldName, modeladmin.FieldFamily, modeladmin.FieldTags, modeladmin.FieldLocation,
		modeladmin.FieldInputPrice, modeladmin.FieldCachePrice, modeladmin.FieldOutputPrice, modeladmin.FieldSubscriptionPrice, modeladmin.FieldSubscriptionPeriod,
	}
	locationChoices = []string{"", "local", "cloud"}
	periodChoices   = []string{"", "month", "year"}
)

// modelFormMode is what a save of the form does.
type modelFormMode int

const (
	modelFormAdd      modelFormMode = iota // n: a new model, every field open
	modelFormRegister                      // enter on a `new` row: provider and name are the row's
	modelFormEdit                          // enter on a registry row: id, provider and name are fixed
)

// modelForm is the add / register / edit form of the Models tab, hand-built
// on bubbles/textinput like the agent form. Text fields are inputs; the three
// fields with a fixed set of values (provider, location, period) are choices
// changed with left and right.
type modelForm struct {
	mode modelFormMode
	// id is the model being edited (modelFormEdit).
	id string
	// providers are the provider choices of an add; choice[f] is the index
	// chosen in field f's list (providers, locationChoices, periodChoices).
	providers []string
	choice    [mfCount]int
	// text are the inputs of the text fields; the choice fields' are unused.
	text [mfCount]textinput.Model
	// initial are the fields' values when the form opened. An edit sends
	// only the fields whose value differs, so an untouched field is never
	// rewritten.
	initial [mfCount]string
	// malformed is true for an edit of a row whose fetch or draft the loader
	// read as absent (config.Model.Malformed): the registry writer refuses
	// to write such a row back, and no field of the form can repair it.
	malformed bool
	cursor    int
	err       string
	saving    bool
}

// modelSavedMsg reports a save of the form.
type modelSavedMsg struct {
	id      string
	changed bool
	notes   []string
	err     error
}

func isChoice(f int) bool { return f == mfProvider || f == mfLocation || f == mfSubPeriod }

// choices are field f's values.
func (f *modelForm) choices(field int) []string {
	switch field {
	case mfProvider:
		return f.providers
	case mfLocation:
		return locationChoices
	}
	return periodChoices
}

// value is field f's current value, as text.
func (f *modelForm) value(field int) string {
	if isChoice(field) {
		return f.choices(field)[f.choice[field]]
	}
	return strings.TrimSpace(f.text[field].Value())
}

// editable reports whether the cursor can land on a field. The provider and
// the model name are fixed once a model exists (or was found on disk): usage
// history, rotation and profiles key on the id they make.
func (f *modelForm) editable(field int) bool {
	return f.mode == modelFormAdd || (field != mfProvider && field != mfName)
}

// formProviders are the providers an add can choose: every registry provider
// and the ones wt can seed a row for when a model names them (openrouter
// among them), without mlx_lm_server — its model is a target+draft pairing,
// which takes two artifacts: `wt model add mlx_lm_server <target> --draft
// <draft>` adds one, and the form has no field for a draft.
func formProviders(cfg *config.Config) []string {
	ids := []string{}
	for _, p := range cfg.Providers {
		ids = append(ids, p.ID)
	}
	for _, id := range config.SeedableProviderIDs() {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	ids = slices.DeleteFunc(ids, localmodels.RunningOnly)
	sort.Strings(ids)
	return ids
}

// formFamilies are the families the registry already uses, offered as
// suggestions for the Family field.
func formFamilies(cfg *config.Config) []string {
	var fams []string
	for _, m := range cfg.Models {
		if m.Family != "" && !slices.Contains(fams, m.Family) {
			fams = append(fams, m.Family)
		}
	}
	sort.Strings(fams)
	return fams
}

// formModel is the registry row behind a table row: the model with the row's
// id under the row's provider. Both, because the tab lists a registry in
// which two rows share an id (modeladmin.Rows pairs them the same way), and
// the form then opens with the values of the row under the cursor, not the
// first one's.
func formModel(cfg *config.Config, row modeladmin.Row) (config.Model, bool) {
	for _, mdl := range cfg.Models {
		if mdl.ID == row.ID && mdl.ProviderID == row.ProviderID {
			return mdl, true
		}
	}
	return config.Model{}, false
}

func priceText(p *float64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatFloat(*p, 'f', -1, 64)
}

// openModelForm opens the form: for a new model (row nil), to register a
// discovered row, or to edit a registry row.
func (m *model) openModelForm(row *modeladmin.Row) {
	cfg := m.models.cfg
	if cfg == nil {
		cfg = &config.Config{}
	}
	f := &modelForm{mode: modelFormAdd, providers: formProviders(cfg)}
	// An add starts on ollama: the provider most models are added to, and
	// one that is always among the choices.
	f.choice[mfProvider] = max(0, slices.Index(f.providers, "ollama"))
	var values [mfCount]string
	switch {
	case row == nil:
	case !row.Registered:
		f.mode = modelFormRegister
		values[mfName] = row.ModelName
		f.providers, f.choice[mfProvider] = []string{row.ProviderID}, 0
	default:
		f.mode, f.id, f.malformed = modelFormEdit, row.ID, len(row.Malformed) > 0
		f.providers, f.choice[mfProvider] = []string{row.ProviderID}, 0
		values[mfName], values[mfFamily], values[mfTags] = row.ModelName, row.Family, strings.Join(row.Tags, ",")
		if mdl, ok := formModel(cfg, *row); ok {
			// The row's own keys, not what they resolve to: a model that
			// inherits its provider's location has none of its own.
			f.choice[mfLocation] = max(0, slices.Index(locationChoices, string(mdl.Location)))
			f.choice[mfSubPeriod] = max(0, slices.Index(periodChoices, mdl.Cost.SubscriptionPeriod))
			values[mfInput], values[mfCache], values[mfOutput] = priceText(mdl.Cost.InputPricePerMillion), priceText(mdl.Cost.CachePricePerMillion), priceText(mdl.Cost.OutputPricePerMillion)
			values[mfSubPrice] = priceText(mdl.Cost.SubscriptionPrice)
		}
	}
	for field := range f.text {
		// The value goes in before the suggestions: bubbles v1.0.0 panics on
		// the accept key when SetValue follows a matched suggestion.
		f.text[field] = newTextInput(values[field], "")
		// No "> " prompt: the label is beside it, and renderFormFields marks
		// the focused row.
		f.text[field].Prompt = ""
	}
	fam := &f.text[mfFamily]
	fam.ShowSuggestions = true
	// The form accepts a suggestion itself (updateModelForm: right, with the
	// cursor at the end of the text). bubbles' own accept key is off: it is
	// tab, which is "next field" here, it fires wherever the cursor is, and
	// it completes what was typed without correcting its case. Its "previous
	// suggestion" key (ctrl+p) is a second v1.0.0 fault: with no suggestion
	// matching it sets the index to -1, which CurrentSuggestion does not
	// guard, so updateModelForm checks the match list and the index before
	// it asks.
	fam.KeyMap.AcceptSuggestion = key.NewBinding(key.WithDisabled())
	fam.SetSuggestions(formFamilies(cfg))
	for field := 0; field < mfCount; field++ {
		f.initial[field] = f.value(field)
	}
	for !f.editable(f.cursor) {
		f.cursor++
	}
	m.models.form = f
	m.models.phase = modelsForm
	m.focusModelField()
	m.resizeModelForm()
}

// focusModelField gives the focus to the field under the cursor, with the
// cursor at the end of its text.
func (m *model) focusModelField() {
	f := m.models.form
	for field := range f.text {
		f.text[field].Blur()
	}
	if !isChoice(f.cursor) {
		f.text[f.cursor].Focus()
		f.text[f.cursor].CursorEnd()
	}
}

// modelFieldRoom is how many columns field's value has beside its label:
// renderFormFields draws "> Label: " in front of it, which is four columns
// past the label's runes.
func (m *model) modelFieldRoom(field int) int {
	return max(m.width-utf8.RuneCountInString(modelFormLabels[field])-4, 4)
}

// resizeModelForm fits each input to the columns left beside its own label —
// not the longest label's, which on a 40-column terminal would leave every
// field sixteen columns.
func (m *model) resizeModelForm() {
	f := m.models.form
	if f == nil {
		return
	}
	for field := range f.text {
		// One column less than the room: a textinput draws its cursor after
		// the text.
		f.text[field].Width = max(m.modelFieldRoom(field)-1, 3)
	}
}

// moveModelField moves the cursor to the next (step 1) or previous (step -1)
// field the user can change, wrapping round.
func (m *model) moveModelField(step int) {
	f := m.models.form
	for {
		f.cursor = (f.cursor + step + mfCount) % mfCount
		if f.editable(f.cursor) {
			break
		}
	}
	m.focusModelField()
}

// updateModelForm handles a message while the form is showing.
func (m *model) updateModelForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	f := m.models.form
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if f.saving {
		// The write is in a command; only ctrl+c is answered meanwhile.
		if k.Type == tea.KeyCtrlC {
			return m.quit()
		}
		return m, nil
	}
	if k.Type != tea.KeyCtrlC && m.modelFormLayout().errorAlone {
		// The refusal had the screen to itself (modelFormView). The key that
		// follows only brings the fields back: one that also typed into a
		// field, or saved, would act on a form the user could not see.
		f.err = ""
		return m, nil
	}
	switch k.Type {
	case tea.KeyCtrlC:
		return m.quit()
	case tea.KeyEsc:
		m.models.phase, m.models.form = modelsList, nil
		return m, nil
	case tea.KeyCtrlS:
		// writing: a quit typed before the write has reported waits for it
		// (leave).
		f.saving, f.err, m.models.writing = true, "", true
		return m, m.saveModelFormCmd()
	case tea.KeyTab, tea.KeyDown, tea.KeyEnter:
		m.moveModelField(1)
		return m, nil
	case tea.KeyShiftTab, tea.KeyUp:
		m.moveModelField(-1)
		return m, nil
	}
	if isChoice(f.cursor) {
		n := len(f.choices(f.cursor))
		switch k.Type {
		case tea.KeyLeft:
			f.choice[f.cursor] = (f.choice[f.cursor] + n - 1) % n
		case tea.KeyRight, tea.KeySpace:
			f.choice[f.cursor] = (f.choice[f.cursor] + 1) % n
		}
		return m, nil
	}
	in := &f.text[f.cursor]
	if f.cursor == mfFamily && k.Type == tea.KeyRight && in.Position() == len([]rune(in.Value())) {
		// Right at the end of the text accepts the suggestion; anywhere else
		// it only moves the cursor. The field takes the suggestion whole:
		// "QW" completed to "QWen3.8" would be a new family beside qwen3.8.
		// bubbles is asked only when it has a suggestion to give: ctrl+p with
		// nothing matching leaves its index at -1, and CurrentSuggestion then
		// indexes the empty match list with it (openModelForm).
		if len(in.MatchedSuggestions()) > 0 && in.CurrentSuggestionIndex() >= 0 {
			if s := in.CurrentSuggestion(); s != "" {
				in.SetValue(s)
				in.CursorEnd()
			}
		}
		return m, nil
	}
	var cmd tea.Cmd
	*in, cmd = in.Update(k)
	return m, cmd
}

// saveModelFormCmd writes the form's model in a command: a registry write
// takes a file lock, and an ollama add first runs `ollama show`.
func (m *model) saveModelFormCmd() tea.Cmd {
	f, deps, cfg := m.models.form, m.opts.Models, m.models.cfg
	given := func(field int) *string {
		v := f.value(field)
		if f.mode == modelFormEdit && v == f.initial[field] {
			return nil // untouched: an edit leaves the key as it is
		}
		if f.mode != modelFormEdit && v == "" && field != mfFamily {
			return nil // an add writes no key for an empty field
		}
		return &v
	}
	fields := modeladmin.Fields{
		Family: given(mfFamily), Tags: given(mfTags), Location: given(mfLocation),
		InputPrice: given(mfInput), CachePrice: given(mfCache), OutputPrice: given(mfOutput),
		SubscriptionPrice: given(mfSubPrice), SubscriptionPeriod: given(mfSubPeriod),
	}
	if f.mode == modelFormEdit {
		id := f.id
		return func() tea.Msg {
			changed, err := modeladmin.Edit(id, fields)
			return modelSavedMsg{id: id, changed: changed, err: err}
		}
	}
	req := modeladmin.AddRequest{ProviderID: f.value(mfProvider), ModelName: f.value(mfName), Fields: fields}
	return func() tea.Msg {
		// What can be refused without the registry is refused before the
		// lookup, which can take its whole timeout when ollama is down.
		if err := modeladmin.CheckAdd(req); err != nil {
			return modelSavedMsg{err: err}
		}
		var notes []string
		if modeladmin.UsesOllama(req.ProviderID) {
			info, err := deps.Capabilities(cfg, req.ModelName)
			switch {
			case errors.Is(err, exec.ErrNotFound):
				// The usual reason, in words that fit a status line.
				notes = append(notes, "added without tool/vision capabilities: ollama is not installed")
			case err != nil:
				notes = append(notes, "added without tool/vision capabilities: "+err.Error())
			}
			req.ModelInfo = info
		}
		res, err := modeladmin.Add(req, deps.SeedEnv())
		if err != nil {
			return modelSavedMsg{err: err}
		}
		for _, p := range res.ProvidersAdded {
			notes = append(notes, "added provider "+p)
		}
		return modelSavedMsg{id: res.ID, changed: true, notes: append(notes, res.Warnings...)}
	}
}

// saveErrorText is a refused save as the form words it: the error, and for a
// row the form opened knowing its fetch or draft is malformed (malformed),
// which the registry writer will not write back as it stands in the file,
// the file to repair, in config.RegistryFixHint's words — nothing in the form
// can fix it. Every other refusal is passed through unchanged: a duplicated
// id (config.ErrModelAmbiguous) and a provider with no row already name the
// file, and a row the writer refuses for a key the form owns — a hand-written
// row with no family, a location that does not resolve — is repaired in the
// form, where a hint to edit the file would send the user away from the
// field that fixes it.
func saveErrorText(err error, malformed bool) string {
	text := err.Error()
	if malformed && errors.Is(err, config.ErrRegistryInvalid) {
		text += " (" + config.RegistryFixHint(config.ErrRegistryEntry) + ")"
	}
	return text
}

// applyModelSaved takes a save's outcome. A refusal stays on the form, with
// the cursor on the field at fault when the refusal names one
// (modeladmin.FieldError); any other refusal — an id the registry holds
// twice, a row whose fetch is malformed — is the save's error and moves
// nothing. A success goes back to the table, which is re-probed with the
// cursor on the saved model, and the routes are owed a sync.
//
// A quit that was waiting for the write (leave) happens now, with the change
// recorded for the caller — through quit, like applyModelRemoved's: agent
// edits the user has not been asked about get the unsaved-changes prompt, and
// ones already given up (discardHeld) are not asked about twice. After a
// refusal the quit does not happen, because the refusal has to be read, and
// the edits a held quit was to discard are unsaved again.
func (m *model) applyModelSaved(msg modelSavedMsg) tea.Cmd {
	mt := &m.models
	mt.writing = false
	f := mt.form
	if f == nil {
		return nil
	}
	f.saving = false
	if msg.err != nil {
		m.refuseHeldQuit()
		f.err = saveErrorText(msg.err, f.malformed)
		var fe *modeladmin.FieldError
		if errors.As(msg.err, &fe) {
			if i := slices.Index(modelFormFields[:], fe.Field); i >= 0 && f.editable(i) {
				f.cursor = i
				m.focusModelField()
			}
		}
		return nil
	}
	verb := "saved "
	switch {
	case f.mode != modelFormEdit:
		verb = "added "
	case !msg.changed:
		verb = "no change to "
	}
	// Each note on a line of its own.
	mt.status = strings.Join(append([]string{verb + msg.id}, msg.notes...), "\n")
	mt.phase, mt.form = modelsList, nil
	m.registryChanged = m.registryChanged || msg.changed
	if m.quitPending {
		m.quitPending, m.discardHeld = false, false
		if _, cmd := m.quit(); cmd != nil {
			return cmd
		}
		// The unsaved-changes prompt is up; the table is read again under
		// it, for a user who cancels.
	}
	if !msg.changed {
		return nil
	}
	mt.selectID = msg.id
	return m.probeCmd()
}

// modelFormHints are the form's key hints, fullest first.
var modelFormHints = []string{
	"ctrl+s save · esc cancel · tab/↑/↓ move · ←/→ change",
	"^s save · esc · ↑/↓ · ←/→ change",
}

// modelFormPairingHints say where an mlx_lm_server pairing is added, fullest
// first: the add form's Provider choice does not offer one (a pairing is two
// artifacts, and the form has one name field), and a user looking for the
// provider there would otherwise find it missing and nothing that says how a
// pairing is added.
var modelFormPairingHints = []string{
	"a pairing: wt model add mlx_lm_server <target> --draft <draft>",
	"wt model add mlx_lm_server T --draft D",
}

// formWindow chooses which of a form's n fields are drawn in room rows: count
// fields from first, always including the one under the cursor. When not all
// of them fit and there are rows to spare for it, the first and the last row
// go to a marker saying how many fields are above and below (above, below),
// so that a field off the screen is never a field the user does not know of.
// Fewer than three rows have none to spare; modelFormLayout keeps the form
// from being drawn in so few when it is an error that took the others.
func formWindow(cursor, n, room int) (first, count int, above, below bool) {
	if room >= n {
		return 0, n, false, false
	}
	if room < 3 {
		// No row to spare for a marker: the fields alone. Only a terminal
		// shorter than wt supports gets here.
		return max(0, cursor-room+1), room, false, false
	}
	for first = 0; first < n; first++ {
		above = first > 0
		rows := room
		if above {
			rows--
		}
		count = min(rows, n-first)
		if below = first+count < n; below {
			count = rows - 1
		}
		if cursor < first+count {
			break
		}
	}
	return first, count, above, below
}

// endCut shortens s to w runes, the last of them an ellipsis.
func endCut(s string, w int) string {
	if runes := []rune(s); len(runes) > w && w > 0 {
		return string(runes[:w-1]) + "…"
	}
	return s
}

// modelFormLayout is what modelFormView draws round the fields, and whether
// there are any fields to draw.
type modelFormLayout struct {
	// head is the tab bar and the title; foot the error, when there is one,
	// and the key hints. errBlock is the error alone, wrapped.
	head, foot, errBlock string
	// room is how many rows the fields have between the two.
	room int
	// errorAlone is true when the error is so long that head and foot leave
	// the fields fewer than three rows — too few for the focused field and
	// the markers that count the fields off the screen (formWindow): the
	// error is then drawn by itself.
	errorAlone bool
}

// modelFormLayout measures the form for the terminal as it is now. The view
// and the key handler both ask it, so that "the error has the screen to
// itself" is one answer.
func (m *model) modelFormLayout() modelFormLayout {
	f := m.models.form
	width := max(m.width, 1)
	title := "Add a model"
	switch f.mode {
	case modelFormRegister:
		title = "Register " + modeladmin.DeriveID(f.value(mfProvider), f.value(mfName))
	case modelFormEdit:
		title = "Edit " + f.id
	}
	switch {
	case f.saving && m.quitPending:
		// The quit was taken; it waits for the write (leave).
		title += "  (saving, then quitting...)"
	case f.saving:
		title += "  (saving...)"
	}
	var l modelFormLayout
	if f.err != "" {
		l.errBlock = tui.ErrorStyle(m.theme).Render(wrapText(f.err, width))
		l.foot = l.errBlock + "\n"
	}
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	l.head = tabBar(m.theme, TabModels) + "\n" + wrapText(title, width) + "\n"
	hints := dim.Render(fitHints(m.width, modelFormHints))
	// While the add form's Provider choice is the field being changed, a
	// line above the key hints says where the provider it lacks is added. It
	// is a row the fields lose on a short terminal, so it is drawn only
	// where they keep three (the focused field and the two markers), and
	// never beside an error, which has its own rule for that below.
	pairing := ""
	if f.mode == modelFormAdd && f.cursor == mfProvider && f.err == "" {
		pairing = dim.Render(fitHints(m.width, modelFormPairingHints)) + "\n"
	}
	l.room = mfCount
	if m.height <= 0 {
		l.head, l.foot = l.head+"\n", "\n"+l.foot+pairing+hints
		return l
	}
	if pairing != "" && m.height-lipgloss.Height(l.head+pairing+hints) < 3 {
		pairing = ""
	}
	l.foot += pairing + hints
	// head ends with a line break, so this is the lines of the two.
	used := lipgloss.Height(l.head + l.foot)
	// A blank line under the title and above the hints, when the terminal
	// has the two rows to spare.
	if m.height >= used+mfCount+2 {
		l.head, l.foot, used = l.head+"\n", "\n"+l.foot, used+2
	}
	l.room = min(mfCount, m.height-used)
	if l.room < 3 && f.err != "" {
		// One or two fields with no marker would read as the whole form, and
		// the error stays until the next save.
		l.errorAlone = true
	}
	if l.room < 1 {
		l.room = 1
	}
	return l
}

// modelFormErrorHint is the key hint under an error that has the screen to
// itself.
const modelFormErrorHint = "press a key to go back to the form"

// modelFormView renders the form inside the terminal: the tab bar, a title,
// as many fields as fit — always the one under the cursor — an error when
// there is one, and the key hints. Bubble Tea drops a too-tall view's top
// lines, so on a short terminal the fields scroll instead, with a marker for
// the ones above and below (formWindow). Nothing is cut silently: the title
// wraps, a fixed value too long for its row loses its middle, and a field
// that is not being edited shows the start of its value and an ellipsis.
//
// A refusal can be longer than a short terminal has lines beside the form —
// an id the registry holds twice is named with both providers and the file to
// fix, nine lines at 40 columns. When it leaves the fields fewer than three
// rows it has the screen to itself, under the tab bar, and the next key
// brings the fields back (updateModelForm): a form squeezed to a field or two
// round it, with no row for a marker, would hide the others unannounced.
func (m *model) modelFormView() string {
	f := m.models.form
	width := max(m.width, 1)
	l := m.modelFormLayout()
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	if l.errorAlone {
		out := tabBar(m.theme, TabModels) + "\n" + l.errBlock
		if lipgloss.Height(out) < m.height {
			out += "\n" + dim.Render(tuilayout.Clip(modelFormErrorHint, width))
		}
		return lipgloss.NewStyle().MaxHeight(m.height).Render(out)
	}
	first, count, above, below := formWindow(f.cursor, mfCount, l.room)
	var fields []formField
	for field := first; field < first+count; field++ {
		value := f.value(field)
		switch {
		case !f.editable(field):
			value = middleCut(value, m.modelFieldRoom(field))
		case isChoice(field):
			shown := value
			if shown == "" {
				shown = map[int]string{mfLocation: "(the provider's)", mfSubPeriod: "(none)"}[field]
			}
			value = "< " + shown + " >"
		case field == f.cursor:
			value = f.text[field].View()
		default:
			value = endCut(value, m.modelFieldRoom(field))
		}
		fields = append(fields, formField{modelFormLabels[field], value, field == f.cursor})
	}
	// renderFormFields ends every row with a newline; the last one's goes,
	// so that clipping does not pad an empty line under the fields.
	rows := tuilayout.Clip(strings.TrimRight(renderFormFields(m.theme, fields), "\n"), width)
	if above {
		rows = dim.Render(fmt.Sprintf("  ↑ %d more", first)) + "\n" + rows
	}
	if below {
		rows += "\n" + dim.Render(fmt.Sprintf("  ↓ %d more", mfCount-first-count))
	}
	return l.head + rows + "\n" + l.foot
}
