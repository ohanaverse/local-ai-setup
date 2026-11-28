package config

// stockAgentList reports whether list is a shape wt itself wrote for the
// agent called name:
//   - any agent: [name, ollama] and [name, ollama, sushi] (the seed and the
//     migrations wrote the first, registering a sushi model appended sushi);
//   - agy: [agy] (the google-to-agy rename left it);
//   - pi and opencode (noNativeAgents, which have no native provider):
//     [ollama] and [ollama, sushi].
//
// Such a list narrows nothing the derived eligibility rule would not offer,
// so ignoring it needs no note.
//
// The judge compares sets, not orders: wt only ever wrote the orders above,
// and a reordering of stock providers narrows nothing either, so reading it
// as a customization would only add a false positive (the note runs on
// every start of every machine). Anything outside the sets, such as another
// provider, a list without ollama on an agent that is not agy, or [ollama]
// on an agent that has a native provider, is a customization.
func stockAgentList(name string, list []string) bool {
	has := map[string]bool{}
	for _, p := range list {
		has[p] = true
	}
	for p := range has {
		if p != name && p != "ollama" && p != "sushi" {
			return false
		}
	}
	if name == "agy" && len(has) == 1 && has["agy"] {
		return true
	}
	if !has["ollama"] {
		return false
	}
	// The agent's own name leads every stock list except pi's and
	// opencode's bare ones, which have no native provider to name.
	return has[name] || noNativeAgents[name]
}

// IgnoredAgentLists names, in file order, the agents whose config.toml entry
// carries a customized supported_providers list. The derived eligibility rule
// reads no list (the field stays decoded and is written back untouched), so a
// person's edit does nothing; callers note it once per process. Silent for
// an entry with no list, a Synthesized agent (it exists only in memory) and
// every shape wt itself wrote (stockAgentList). default_provider is not
// judged separately.
func (c *Config) IgnoredAgentLists() []string {
	if c == nil {
		return nil
	}
	var out []string
	for _, a := range c.Agents {
		if a.Synthesized || len(a.SupportedProviders) == 0 {
			continue
		}
		if !stockAgentList(a.Name, a.SupportedProviders) {
			out = append(out, a.Name)
		}
	}
	return out
}
