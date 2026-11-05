package cleanup

// Rank is a status's place in the safety order: proven 0, less_sure 1,
// unique_work 2, in_use 3. Lower is safer. A value Classify never makes
// ranks worst (4), so it is never mistaken for a safer status. It is also
// the display order of the groups.
func (s Status) Rank() int {
	switch s {
	case StatusProven:
		return 0
	case StatusLessSure:
		return 1
	case StatusUniqueWork:
		return 2
	case StatusInUse:
		return 3
	}
	return 4
}

// WorseThan reports whether s is less safe than o.
func (s Status) WorseThan(o Status) bool { return s.Rank() > o.Rank() }
