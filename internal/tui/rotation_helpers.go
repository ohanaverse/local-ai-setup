package tui

// oppositeTag returns the other rotation group name for the `d` toggle.
// For the current code/design setup that's a literal swap. Unknown tags
// return "" so the toggle no-ops: ModelsForAgentAndTag(agent, "") finds
// no models and the empty-tag guard restores the previous tag.
func oppositeTag(tag string) string {
	switch tag {
	case "code":
		return "design"
	case "design":
		return "code"
	default:
		return ""
	}
}
