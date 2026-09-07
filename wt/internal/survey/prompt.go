package survey

import (
	"bufio"
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/x/term"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// stdinTTY is a test seam wrapping the real TTY check, mirroring cmd/wt's
// isStdinTTY/stdinTTY pattern. PromptRun's r/w are injected for tests, but
// TTY-ness is a property of the real process's stdin, not of whatever
// io.Reader a test passes in — so it needs its own seam rather than
// inferring TTY-ness from r.
var stdinTTY = func() bool { return term.IsTerminal(os.Stdin.Fd()) }

// answer identifies the Q1 verdict.
type answer int

const (
	answerSkip answer = iota
	answerNo
	answerYes
)

// PromptRun runs the up-to-three-question post-session survey against r/w,
// records the answer via store, and prints the accumulated stats. It is a
// no-op when stdin is not a TTY (non-interactive launch) or when m.ID == ""
// (command agents like shell have no model to survey) — the single guard
// both the TUI and non-TUI launch paths rely on.
func PromptRun(r io.Reader, w io.Writer, store Store, agent string, m config.Model) {
	if !stdinTTY() || m.ID == "" {
		return
	}
	scanner := bufio.NewScanner(r)

	verdict, ok := promptWorked(scanner, w)
	if !ok {
		return // reader exhausted (e.g. closed pipe); nothing to record
	}

	var e Event
	switch verdict {
	case answerSkip:
		e = Event{Agent: agent, ModelID: m.ID, Skipped: true}
	case answerNo:
		e = Event{Agent: agent, ModelID: m.ID, Worked: boolPtr(false)}
	case answerYes:
		speed := promptRating(scanner, w, "speed 1(slow)-5(fast)?")
		quality := promptRating(scanner, w, "quality 1(bad)-5(great)?")
		e = Event{Agent: agent, ModelID: m.ID, Worked: boolPtr(true), Speed: speed, Quality: quality}
	}

	if err := store.Record(e); err != nil {
		fmt.Fprintf(os.Stderr, "wt: survey not saved: %v\n", err)
		return
	}
	fmt.Fprintln(w, "survey saved")
	fmt.Fprintln(w, FormatAfterSurvey(store.Events(), agent, m.ID, now().UTC()))
}

// promptWorked runs Q1's reprompt loop. The bool return is false only when
// the scanner is exhausted without a valid line (e.g. a closed reader in a
// test) — a real TTY session always eventually yields a valid answer or an
// Enter (skip).
func promptWorked(scanner *bufio.Scanner, w io.Writer) (answer, bool) {
	for {
		fmt.Fprint(w, "survey · did it work? [y]es / [n]o / [s]kip (Enter=skip) ")
		if !scanner.Scan() {
			return answerSkip, false
		}
		switch scanner.Text() {
		case "y", "Y":
			return answerYes, true
		case "n", "N":
			return answerNo, true
		case "s", "S", "":
			return answerSkip, true
		default:
			fmt.Fprintln(w, "please answer y, n, s, or Enter to skip")
		}
	}
}

// promptRating runs one Q2/Q3 reprompt loop. Returns nil when the user
// presses Enter (leave unrated) or when the scanner is exhausted.
func promptRating(scanner *bufio.Scanner, w io.Writer, question string) *int {
	for {
		fmt.Fprintf(w, "survey · %s (Enter=skip) ", question)
		if !scanner.Scan() {
			return nil
		}
		text := scanner.Text()
		if text == "" {
			return nil
		}
		if len(text) == 1 && text[0] >= '1' && text[0] <= '5' {
			v := int(text[0] - '0')
			return &v
		}
		fmt.Fprintln(w, "please answer 1-5, or Enter to skip")
	}
}
