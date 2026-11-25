package split

import (
	"testing"
)

func TestComputeShares(t *testing.T) {
	cases := []struct {
		name      string
		base      int64
		receivers []Receiver
		want      []Share
	}{
		{
			name: "bps + fixed",
			base: 10000,
			receivers: []Receiver{
				{Account: "partner-a", RateBps: 1000},
				{Account: "partner-b", RateBps: 500, FixedMinor: 100},
			},
			want: []Share{
				{Receiver: "partner-a", Amount: 1000},
				{Receiver: "partner-b", Amount: 600},
			},
		},
		{
			name: "capped at base",
			base: 1000,
			receivers: []Receiver{
				{Account: "a", RateBps: 10000},
				{Account: "b", RateBps: 10000},
			},
			want: []Share{
				{Receiver: "a", Amount: 1000},
				{Receiver: "b", Amount: 0},
			},
		},
		{
			name:      "empty receivers",
			base:      5000,
			receivers: nil,
			want:      nil,
		},
		{
			name: "non-positive base",
			base: 0,
			receivers: []Receiver{
				{Account: "a", RateBps: 1000},
			},
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeShares(tc.base, tc.receivers)
			if len(got) != len(tc.want) {
				t.Fatalf("len(got)=%d len(want)=%d", len(got), len(tc.want))
			}
			var total int64
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("index %d: got %+v want %+v", i, got[i], tc.want[i])
				}
				total += got[i].Amount
			}
			if total > tc.base {
				t.Fatalf("total split %d exceeds base %d", total, tc.base)
			}
		})
	}
}
