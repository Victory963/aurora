package poolmath

import (
	"fmt"
	"testing"
)

func TestCompute_TwoSided_RakeAndPayout(t *testing.T) {
	// Matches smoke_m6: A 1000 on YES, B 600 on NO, YES wins, rake 5%.
	bets := []BetIn{
		{ID: "a", OptionID: "yes", UserID: "A", StakeMinor: 1000},
		{ID: "b", OptionID: "no", UserID: "B", StakeMinor: 600},
	}
	out := Compute(bets, "yes", 500)
	if out.RakeMinor != 30 {
		t.Fatalf("rake = %d, want 30", out.RakeMinor)
	}
	if got := out.Results["a"]; got.Status != StatusWon || got.PayoutMinor != 1570 {
		t.Fatalf("winner = %+v, want WON/1570", got)
	}
	if got := out.Results["b"]; got.Status != StatusLost || got.PayoutMinor != 0 {
		t.Fatalf("loser = %+v, want LOST/0", got)
	}
	if out.Winners != 1 || out.Refunded || out.TotalStakeMinor != 1600 {
		t.Fatalf("outcome meta wrong: %+v", out)
	}
}

func TestCompute_NoWinners_RefundsEveryone(t *testing.T) {
	bets := []BetIn{
		{ID: "a", OptionID: "no", StakeMinor: 500},
		{ID: "b", OptionID: "no", StakeMinor: 700},
	}
	out := Compute(bets, "yes", 500)
	if !out.Refunded || out.RakeMinor != 0 {
		t.Fatalf("expected full refund, got %+v", out)
	}
	for id, want := range map[string]int64{"a": 500, "b": 700} {
		if r := out.Results[id]; r.Status != StatusRefunded || r.PayoutMinor != want {
			t.Fatalf("bet %s = %+v, want REFUNDED/%d", id, r, want)
		}
	}
}

func TestCompute_NoLosers_StakeBack(t *testing.T) {
	bets := []BetIn{{ID: "a", OptionID: "yes", StakeMinor: 900}}
	out := Compute(bets, "yes", 500)
	if r := out.Results["a"]; r.Status != StatusWon || r.PayoutMinor != 900 {
		t.Fatalf("got %+v, want WON/900 (stake back)", r)
	}
	if out.RakeMinor != 0 {
		t.Fatalf("rake should be 0 with no losers, got %d", out.RakeMinor)
	}
}

func TestCompute_ProRataSplitWithDust(t *testing.T) {
	// winners 100 + 50, losers 100, rake 5% => rake 5, net 95.
	// shares: floor(95*100/150)=63, floor(95*50/150)=31 → dust 1 to the house.
	bets := []BetIn{
		{ID: "w1", OptionID: "yes", StakeMinor: 100},
		{ID: "w2", OptionID: "yes", StakeMinor: 50},
		{ID: "l1", OptionID: "no", StakeMinor: 100},
	}
	out := Compute(bets, "yes", 500)
	if out.Results["w1"].PayoutMinor != 163 || out.Results["w2"].PayoutMinor != 81 {
		t.Fatalf("shares wrong: w1=%d w2=%d", out.Results["w1"].PayoutMinor, out.Results["w2"].PayoutMinor)
	}
	paid := out.Results["w1"].PayoutMinor + out.Results["w2"].PayoutMinor
	// money conservation: payouts ≤ total stake - rake (dust stays with house)
	if paid+out.RakeMinor > out.TotalStakeMinor {
		t.Fatalf("created money: paid=%d rake=%d total=%d", paid, out.RakeMinor, out.TotalStakeMinor)
	}
}

func TestCompute_LargeStakes_NoOverflow(t *testing.T) {
	const big int64 = 1_000_000_000_000_000 // wallet per-op cap (1e15)
	bets := []BetIn{
		{ID: "w", OptionID: "yes", StakeMinor: big},
		{ID: "l", OptionID: "no", StakeMinor: big},
	}
	out := Compute(bets, "yes", 500)
	want := big + (big - big*500/10_000) // stake + net loser pool
	if out.Results["w"].PayoutMinor != want {
		t.Fatalf("payout = %d, want %d", out.Results["w"].PayoutMinor, want)
	}
	if out.Results["w"].PayoutMinor <= 0 {
		t.Fatal("overflowed")
	}
}

func TestCompute_ManyMaxBets_RakeDoesNotOverflow(t *testing.T) {
	// Regression: 20 losing bets of 1e15 => loserPool 2e16; the old int64 rake
	// product (2e16 * 500 = 1e19 > MaxInt64) wrapped negative and MINTED money.
	const max = 1_000_000_000_000_000
	bets := []BetIn{{ID: "w", OptionID: "yes", StakeMinor: 1000}}
	for i := 0; i < 20; i++ {
		bets = append(bets, BetIn{ID: fmt.Sprintf("l%d", i), OptionID: "no", StakeMinor: max})
	}
	out := Compute(bets, "yes", 500)
	const loserPool = int64(20 * max)
	wantRake := loserPool / 20 // 2e16 * 5% = 1e15
	if out.RakeMinor != wantRake {
		t.Fatalf("rake = %d, want %d (must not wrap negative)", out.RakeMinor, wantRake)
	}
	if out.RakeMinor < 0 {
		t.Fatal("rake wrapped negative (int64 overflow)")
	}
	// Conservation: winner payout = stake + net; net = loserPool - rake.
	wantPayout := int64(1000) + (loserPool - wantRake)
	if got := out.Results["w"].PayoutMinor; got != wantPayout {
		t.Fatalf("payout = %d, want %d", got, wantPayout)
	}
}
