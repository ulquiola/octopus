package op

import (
	"testing"
	"time"
)

func TestStatsPeriodSince(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.Local)
	cases := []struct {
		period int
		want   string
	}{
		{1, "20261026"},
		{7, "20261020"},
		{30, "20260927"},
	}
	for _, tc := range cases {
		if got := statsPeriodSince(tc.period, now); got != tc.want {
			t.Fatalf("period %d: got %s, want %s", tc.period, got, tc.want)
		}
	}
}