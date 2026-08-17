package tui

// oppositeTag returns the other rotation group name. For the current
// code/design setup that's a literal swap. Unknown tags return "" so
// rotation.ForTag.Next("") is a no-skip call rather than skipping the
// current tag's last-used model by accident.
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
