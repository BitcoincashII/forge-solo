package tides

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// TIDES as OCEAN defines it (https://ocean.xyz/docs/tides): "share_log_window is eight times the
// block's difficulty worth of shares", "Each proof counts as a number of shares in the share log
// equal to the proof's target difficulty", and the reward is "split up proportionally". Forge
// works the split out when work is built -- OCEAN: "rewards can be pre-calculated when work is
// given to a miner" -- so on-chain payouts need no pool wallet: a found block's coinbase is the
// payout.

// WindowMultiple is the TIDES share-log window in units of network difficulty.
const WindowMultiple = 8.0

// DustSats is the smallest amount a miner is paid in a coinbase output: 546 satoshis, the
// Bitcoin Core dust threshold for a P2PKH output. A smaller amount is carried to the miner's next
// payout instead (see Split).
const DustSats int64 = 546

var (
	ErrNoWork   = errors.New("tides: no pooled work in the share-log window")
	ErrBadInput = errors.New("tides: invalid split input")
)

// Output is one coinbase output of a TIDES split.
type Output struct {
	Address string `json:"address"`
	Sats    int64  `json:"sats"`
	Fee     bool   `json:"fee,omitempty"`
}

// Split divides a coinbase value between the pool fee and the miners in the window.
//
//   - The fee output gets floor(value × feePct / 100) and comes first (it is left out when 0).
//   - Each miner is due its carried balance plus a base share of the rest in proportion to its
//     work. Miners whose due reaches dust are paid in this coinbase; the others get no output and
//     carry their due forward in newCarry, so the pool never holds anything.
//   - The paid miners' base shares are scaled so the coinbase pays out exactly value: what is
//     withheld as carry this block goes to the paid miners in proportion to their work, and
//     carried balances being paid out are funded the same way. The difference from an exact
//     proportional split is bounded by the total carried balance, i.e. by a few dust amounts.
//   - Amounts are floored to satoshis. The leftover satoshis go to the paid miner with the largest
//     amount (ties broken by address), and miner outputs are sorted by address, so every builder
//     derives the identical coinbase from the same inputs.
//   - Carried balances of miners who are not in the window are kept, not paid.
func Split(value int64, work map[string]float64, carry map[string]int64, feePct float64, feeAddr string, dust int64) ([]Output, map[string]int64, error) {
	if value <= 0 || dust < 0 || !(feePct >= 0 && feePct < 100) {
		return nil, nil, fmt.Errorf("%w: value=%d fee=%v dust=%d", ErrBadInput, value, feePct, dust)
	}
	var total float64
	addrs := make([]string, 0, len(work))
	for a, w := range work {
		if a == "" || math.IsNaN(w) || math.IsInf(w, 0) || w < 0 {
			return nil, nil, fmt.Errorf("%w: work %v for %q", ErrBadInput, w, a)
		}
		if w > 0 {
			addrs = append(addrs, a)
			total += w
		}
	}
	for a, c := range carry {
		if c < 0 {
			return nil, nil, fmt.Errorf("%w: negative carry %d for %q", ErrBadInput, c, a)
		}
	}
	if total <= 0 {
		return nil, nil, ErrNoWork
	}
	sort.Strings(addrs)

	fee := int64(math.Floor(float64(value) * feePct / 100))
	if fee > 0 && feeAddr == "" {
		return nil, nil, fmt.Errorf("%w: a fee but no fee address", ErrBadInput)
	}
	rest := value - fee

	base := make(map[string]float64, len(addrs))
	for _, a := range addrs {
		base[a] = float64(rest) * work[a] / total
	}

	// Payable miners: everyone whose unscaled due reaches dust, then drop anyone the scaling pushes
	// under it, until the set is stable. Carried balances are only paid while the rest can fund them.
	payable := make(map[string]bool, len(addrs))
	for _, a := range addrs {
		if float64(carry[a])+base[a] >= float64(dust) {
			payable[a] = true
		}
	}
	useCarry := true
	var k float64
	for {
		var carryP, baseP float64
		for a := range payable {
			carryP += float64(carry[a])
			baseP += base[a]
		}
		if len(payable) == 0 {
			break
		}
		if carryP >= float64(rest) {
			useCarry = false // cannot happen with dust-sized carries; keep them for a later block
			carryP = 0
		}
		k = (float64(rest) - carryP) / baseP
		changed := false
		for a := range payable {
			due := base[a] * k
			if useCarry {
				due += float64(carry[a])
			}
			if due < float64(dust) {
				delete(payable, a)
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	if len(payable) == 0 {
		// Every due is below dust (only possible with a tiny coinbase): pay the whole rest to the
		// miner with the most work so the coinbase still pays out in full.
		top := addrs[0]
		for _, a := range addrs[1:] {
			if work[a] > work[top] {
				top = a
			}
		}
		payable[top] = true
		useCarry = false
		k = float64(rest) / base[top]
	}

	amounts := make(map[string]int64, len(payable))
	var paid int64
	for _, a := range addrs {
		if !payable[a] {
			continue
		}
		due := base[a] * k
		if useCarry {
			due += float64(carry[a])
		}
		amt := int64(math.Floor(due))
		amounts[a] = amt
		paid += amt
	}
	if leftover := rest - paid; leftover != 0 {
		var big string
		for _, a := range addrs {
			if payable[a] && (big == "" || amounts[a] > amounts[big]) {
				big = a
			}
		}
		amounts[big] += leftover
	}

	outputs := make([]Output, 0, len(amounts)+1)
	if fee > 0 {
		outputs = append(outputs, Output{Address: feeAddr, Sats: fee, Fee: true})
	}
	for _, a := range addrs {
		if payable[a] {
			outputs = append(outputs, Output{Address: a, Sats: amounts[a]})
		}
	}

	newCarry := make(map[string]int64)
	for a, c := range carry {
		if c > 0 && !(payable[a] && useCarry) {
			newCarry[a] = c
		}
	}
	for _, a := range addrs {
		if !payable[a] {
			newCarry[a] += int64(math.Floor(base[a]))
		}
	}
	return outputs, newCarry, nil
}
