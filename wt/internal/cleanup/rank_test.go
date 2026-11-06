package cleanup

import "testing"

// TestStatusRankOrder pins the safety order proven < less_sure < unique_work
// < in_use, and that a status the classifier does not know ranks worst. Why:
// removal compares a row's status at removal time with the one the user
// approved; an unknown value ranking safe (or as -1) would let a row that got
// worse in between be removed under an older, safer approval.
func TestStatusRankOrder(t *testing.T) {
	order := []Status{StatusProven, StatusLessSure, StatusUniqueWork, StatusInUse}
	for i, s := range order {
		if got := s.Rank(); got != i {
			t.Errorf("%s.Rank() = %d, want %d", s, got, i)
		}
	}
	for _, s := range []Status{"", "PROVEN", "gone"} {
		if got := s.Rank(); got != 4 {
			t.Errorf("Status(%q).Rank() = %d, want 4 (worst)", s, got)
		}
	}
}

// TestStatusWorseThan pins WorseThan as a strict comparison of ranks. Why:
// the removal engine refuses a row whose status became worse than approved;
// equal must not count as worse, and an unknown status is worse than any.
func TestStatusWorseThan(t *testing.T) {
	cases := []struct {
		a, b Status
		want bool
	}{
		{StatusLessSure, StatusProven, true},
		{StatusInUse, StatusUniqueWork, true},
		{StatusProven, StatusProven, false},
		{StatusProven, StatusLessSure, false},
		{"bogus", StatusInUse, true},
		{StatusInUse, "bogus", false},
	}
	for _, c := range cases {
		if got := c.a.WorseThan(c.b); got != c.want {
			t.Errorf("%q.WorseThan(%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
