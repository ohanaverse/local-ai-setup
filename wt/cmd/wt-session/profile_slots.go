// The slot-profile halves of `wt profile list` and `wt profile show`. See
// docs/wt-agents/slot-profiles.md.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// writeSlotProfileList prints the slot-profile section of `wt profile list`:
// one line per profile of slot-profiles.toml, in file order, with its agent,
// its id and "ok" or its static error. A profile with no usable name or agent
// is listed by its position in the file. The verdict is the static one only
// (profiles.ValidateSlots): no provider is probed, so a profile that is "ok"
// here may still be blocked in the picker; `wt profile show` says. After the
// profiles comes one "warning:" line per key of the file the schema does not
// name, also when the file gave no profile, then one per driver warning
// about a passing profile's config_content (slotContentNotes), which leaves
// it "ok". It reads no registry and asks no model server (a session path
// that needs none): a model server whose catalog holds the "profile/" prefix
// blocks the profile's launch and `wt profile show` with its own reason.
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
	for _, note := range slotContentNotes(a) {
		fmt.Fprintf(w, "  warning: %s\n", note)
	}
	return nil
}

// writeSlotProfileShow prints `wt profile show -M profile/<name>` for row, on
// either path: the verdict, a blocked row's reason (and an error), the slots,
// the driver's config_content notes and the command the agent's slot build
// returns for origin and key. key is always masked by the caller.
func writeSlotProfileShow(w io.Writer, agent, id string, row catalog.Row, origin, key string) error {
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
	if p.Check.Alias.Rest != "" {
		fmt.Fprintf(w, "default: %s — %s\n", sp.Default, p.Check.Alias.Doc)
	}
	for _, slot := range p.Check.Declared {
		m := p.Models[slot]
		name := slot
		if slot == p.Check.RestSlot() {
			name += "*"
		}
		line := fmt.Sprintf("slot %s: %s (%s)", name, m.ID, m.ModelName)
		if _, filled := sp.Slots[slot]; !filled {
			line += " [default's model]"
		}
		fmt.Fprintln(w, line)
	}
	// What the driver says about this profile's config_content goes right
	// under the slots: claude's notes name an override target that is not
	// one of the slot models just listed (#419).
	for _, note := range slotContentNotesFor(p.Check) {
		fmt.Fprintf(w, "warning: %s\n", note)
	}

	sc, err := agents.BuildSlotCommand(p.Launch(), false, origin, key)
	if err != nil {
		return err
	}
	for _, kv := range sc.Env {
		fmt.Fprintf(w, "env: %s\n", kv)
	}
	// Header mode (a subscription slot, #597): the token travels as one line
	// of ANTHROPIC_CUSTOM_HEADERS, shown masked. The caller passed a masked
	// key already; masking again changes nothing and keeps this line safe
	// for any caller.
	if sc.HeaderToken != "" {
		fmt.Fprintf(w, "header: %s: %s\n", profiles.WtHeaderName, config.MaskKey(sc.HeaderToken))
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

// showStill is what a `wt profile show` that could not ask the model server
// says is still true.
const showStill = "nothing was shown"

// showSlotProfileRemote is showSlotProfile on a client session: the profile
// is resolved against the model server's catalog (one Catalog read,
// catalog.ResolveSlotProfileWire), exactly as a launch of it would be, and the
// preview of the command passes the slot build the gateway origin and the
// client token masked (config.MaskKey). The [litellm] url and key are host state a
// client session never reads (R4), so neither can appear in the output. A
// server that does not answer fails the command (exit 1).
func showSlotProfileRemote(ctx context.Context, w io.Writer, a *app, agent, id string) error {
	if a.slotLoadErr != nil {
		return a.slotLoadErr
	}
	name := strings.TrimPrefix(id, profiles.SlotIDPrefix)
	var check *profiles.SlotCheck
	for i, c := range a.slotChecks {
		if c.Usable && c.Profile.Agent == agent && c.Profile.Name == name {
			check = &a.slotChecks[i]
			break
		}
	}
	if check == nil {
		return fmt.Errorf("slot profile %q is not defined for agent %q", id, agent)
	}
	ms, err := a.openModels(ctx, showStill)
	if err != nil {
		return remoteExit(err) // openModels' errors carry the still clause
	}
	cat, err := ms.client.Catalog(ctx)
	if err != nil {
		return remoteExit(modelclient.Still(err, showStill))
	}
	if holder := catalog.SlotPrefixHolderWire(cat.Rows); holder != "" {
		return errors.New(slotPrefixHeldReason(holder))
	}
	row := catalog.ResolveSlotProfileWire(catalog.WireByID(cat.Rows), *check)
	return writeSlotProfileShow(w, agent, id, row, strings.TrimRight(ms.ep.Origin(), "/"), config.MaskKey(ms.ep.Token))
}
