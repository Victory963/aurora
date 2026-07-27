// Package poolmath implements the closed-pool (parimutuel) settlement math as
// a pure function so it is unit-testable without a database (ADR-0006 §1).
//
// Rules:
//   - losers' pool minus rake is split among winners pro-rata to stake;
//     winners also get their stake back. Integer minor units, floor division,
//     rounding dust implicitly stays with the house (wallet mint absorbs it).
//   - no winners  -> everyone is REFUNDED their stake, zero rake.
//   - no losers   -> winners get exactly their stake back (net pool is zero).
package poolmath

import "math/big"

const (
	StatusWon      = "WON"
	StatusLost     = "LOST"
	StatusRefunded = "REFUNDED"
)

type BetIn struct {
	ID         string
	OptionID   string
	UserID     string
	StakeMinor int64
}

type Result struct {
	Status      string
	PayoutMinor int64
}

type Outcome struct {
	Results         map[string]Result // keyed by bet ID
	TotalStakeMinor int64
	RakeMinor       int64
	Winners         int
	Refunded        bool
}

// Compute settles a pool. Deterministic; safe to re-run on the same inputs.
//
// ALL aggregates are big.Int: per-bet stakes are capped at 1e15 by the server,
// but nothing caps the bet COUNT, so int64 sums/products (loser pool × rakeBps
// in particular) can overflow and would mint money. Conversions back to int64
// saturate at MaxInt64 — a payout that large is rejected by the wallet's
// per-op cap, so settlement fails loudly (502, resumable) instead of paying a
// silently wrapped amount.
func Compute(bets []BetIn, winningOptionID string, rakeBps int) Outcome {
	out := Outcome{Results: make(map[string]Result, len(bets))}

	winnerTotal, loserPool, total := new(big.Int), new(big.Int), new(big.Int)
	for _, b := range bets {
		stake := big.NewInt(b.StakeMinor)
		total.Add(total, stake)
		if b.OptionID == winningOptionID {
			winnerTotal.Add(winnerTotal, stake)
		} else {
			loserPool.Add(loserPool, stake)
		}
	}
	out.TotalStakeMinor = saturateInt64(total)

	if winnerTotal.Sign() == 0 {
		// Nobody picked the winning side: refund everyone, take no rake.
		out.Refunded = true
		for _, b := range bets {
			out.Results[b.ID] = Result{Status: StatusRefunded, PayoutMinor: b.StakeMinor}
		}
		return out
	}

	rake := new(big.Int).Mul(loserPool, big.NewInt(int64(rakeBps)))
	rake.Div(rake, big.NewInt(10_000))
	out.RakeMinor = saturateInt64(rake)
	net := new(big.Int).Sub(loserPool, rake)

	for _, b := range bets {
		if b.OptionID != winningOptionID {
			out.Results[b.ID] = Result{Status: StatusLost, PayoutMinor: 0}
			continue
		}
		out.Winners++
		// share = net * stake / winnerTotal
		share := new(big.Int).Mul(net, big.NewInt(b.StakeMinor))
		share.Div(share, winnerTotal)
		payout := share.Add(share, big.NewInt(b.StakeMinor))
		out.Results[b.ID] = Result{Status: StatusWon, PayoutMinor: saturateInt64(payout)}
	}
	return out
}

func saturateInt64(v *big.Int) int64 {
	if !v.IsInt64() {
		return int64(^uint64(0) >> 1) // math.MaxInt64
	}
	return v.Int64()
}
