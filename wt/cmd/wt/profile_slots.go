// The slot-profile halves of `wt profile list` and `wt profile show`. See
// docs/wt-agents/slot-profiles.md.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
)

// maskKey is how wt shows the LiteLLM proxy key: "(unset)" when empty, "***"
// for a key of four characters or fewer (its last four would be the whole
// secret), "***" plus the last four otherwise.
func maskKey(key string) string {
	switch {
	case key == "":
		return "(unset)"
	case len(key) <= 4:
		return "***"
	}
	return "***" + key[len(key)-4:]
}

// writeSlotProfileList prints the slot-profile section of `wt profile list`:
// one line per profile of slot-profiles.toml, in file order, with its agent,
// its id and "ok" or its static error. A profile with no usable name or agent
// is listed by its position in the file. The verdict is the static one only
// (profiles.ValidateSlots): no provider is probed, so a profile that is "ok"
// here may still be blocked in the picker; `wt profile show` says. After the
// profiles comes one "warning:" line per key of the file the schema does not
// name, also when the file gave no profile, and one when the registry holds
// the "profile/" prefix (slotReservedNote), since every profile listed as
// "ok" is then without a row.
//
// It reads nothing of profiles.toml. It returns a.slotLoadErr after printing
// it.
func writeSlotProfileList(w io.Writer, a *app) error {
	if a.slotLoadErr != nil {
		fmt.Fprintf(w, "slot profiles: %v\n", a.slotLoadErr)
		return a.slotLoadErr
	}
	if len(a.slotChecks) == 0 {
		fmt.Fprintln(w, "slot profiles: (none defined)")
	} else {
		fmt.Fprintln(w, "slot profiles:")
	}
	for _, c := range a.slotChecks {
		switch {
		case !c.Usable:
			fmt.Fprintf(w, "  %s: error: %v\n", c.Label(), c.Err)
		case c.Err != nil:
			fmt.Fprintf(w, "  %s: %s error: %v\n", c.Profile.Agent, c.Label(), c.Err)
		default:
			fmt.Fprintf(w, "  %s: %s ok\n", c.Profile.Agent, c.Label())
		}
	}
	for _, key := range a.slotUnknown {
		fmt.Fprintf(w, "  warning: %s\n", slotUnknownKeyNote(key))
	}
	if note := slotReservedNote(a); note != "" {
		fmt.Fprintf(w, "  warning: %s\n", note)
	}
	return nil
}

// showSlotProfile is `wt profile show -A <agent> -M profile/<name>`. It builds
// the agent's catalog rows from one inventory probe, exactly as the picker
// does, and looks the profile's row up with catalog.Find, so the verdict it
// prints is the picker's. For a launch or a start row it prints each declared
// slot with its registry id and provider-side name, then the command the
// agent's slot build returns: the env it sets, the inherited variables it
// removes, the arguments, and the --settings payload.
//
// The slot build is given the masked key, never the real one, so the proxy
// key cannot appear in the output. A blocked profile prints its reason and
// returns an error. Nothing is started, no route is written and the proxy is
// not restarted. It reads nothing of profiles.toml.
func showSlotProfile(w io.Writer, a *app, agent, id string) error {
	if a.slotLoadErr != nil {
		return a.slotLoadErr
	}
	if a.cfg == nil {
		return fmt.Errorf("no config loaded")
	}
	eligible, err := a.cfg.EligibleModels(agent, "", "")
	if err != nil {
		return err
	}
	snap := probeInventory(a.cfg)
	rows := catalog.Build(catalog.Input{
		Config: a.cfg, Agent: agent, Models: eligible, Inventory: &snap,
		SlotProfiles: a.slotChecks,
	})
	row, ok := catalog.Find(rows, id)
	if !ok || !row.IsProfile() {
		return fmt.Errorf("slot profile %q is not defined for agent %q", id, agent)
	}

	verdict := "launch"
	switch row.Action() {
	case catalog.ActionStart:
		verdict = "start"
	case catalog.ActionBlock:
		verdict = "blocked"
	}
	fmt.Fprintf(w, "%s (%s): %s\n", id, agent, verdict)
	if row.Action() == catalog.ActionBlock {
		fmt.Fprintf(w, "reason: %s\n", row.BlockReason())
		return fmt.Errorf("%s is blocked: %s", id, row.BlockReason())
	}

	p := row.Profile
	sp := p.Check.Profile
	for _, slot := range p.Check.Declared {
		m := p.Models[slot]
		name := slot
		if slot == sp.Default {
			name += "*"
		}
		line := fmt.Sprintf("slot %s: %s (%s)", name, m.ID, m.ModelName)
		if _, filled := sp.Slots[slot]; !filled {
			line += " [default's model]"
		}
		fmt.Fprintln(w, line)
	}

	sc, err := agents.BuildSlotCommand(p.Launch(), false, a.cfg.LitellmBaseURL(), maskKey(a.cfg.LitellmAPIKey()))
	if err != nil {
		return err
	}
	for _, kv := range sc.Env {
		fmt.Fprintf(w, "env: %s\n", kv)
	}
	for _, name := range sc.ClearEnv {
		fmt.Fprintf(w, "unset: %s\n", name)
	}
	for _, prefix := range sc.ClearEnvPrefixes {
		fmt.Fprintf(w, "unset: %s*\n", prefix)
	}
	// The --settings value is one long JSON argument: it is shown as
	// <settings> on the args line and in full, indented, below it.
	args := slices.Clone(sc.Args)
	settings := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--settings" {
			settings = args[i+1]
			args[i+1] = "<settings>"
		}
	}
	fmt.Fprintf(w, "args: %s\n", strings.Join(args, " "))
	if settings != "" {
		fmt.Fprintln(w, "settings:")
		var buf bytes.Buffer
		if json.Indent(&buf, []byte(settings), "  ", "  ") == nil {
			settings = buf.String()
		}
		fmt.Fprintf(w, "  %s\n", settings)
	}
	return nil
}
