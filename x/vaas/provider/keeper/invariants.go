package keeper

import (
	"fmt"
	"strings"

	types "github.com/allinbits/vaas/x/vaas/provider/types"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// RegisterInvariants registers all provider module invariants
func RegisterInvariants(ir sdk.InvariantRegistry, k *Keeper) {
	ir.RegisterRoute(types.ModuleName, "fee-pool-shares-consistency",
		FeePoolSharesConsistencyInvariant(*k))
}

// FeePoolSharesConsistencyInvariant checks the per-consumer fee-pool share
// accounting against itself and against bank balances. For every
// (consumer, denom) pair it asserts:
//
//   - sum(ConsumerFeePoolShares[(consumer, denom, *)])
//     == ConsumerFeePoolTotalShares[(consumer, denom)]
//   - shares exist iff a total entry exists (no orphan rows on either side)
//   - total_shares > 0 implies bank balance > 0 at the pool address
//     (lazy-invalidation invariant: balance 0 with shares is reconciled on
//     the next MintShares, but must not be allowed to persist post-block
//     as a steady state)
//   - bank balance > 0 implies total_shares > 0 (orphan-balance invariant:
//     enforced at genesis and at every MintShares/Sweep call site)
func FeePoolSharesConsistencyInvariant(k Keeper) sdk.Invariant {
	return func(ctx sdk.Context) (string, bool) {
		var msgs []string

		// Pass 1: sum shares per (consumer, denom).
		sums := map[uint64]map[string]math.Int{}
		shareIter, err := k.ConsumerFeePoolShares.Iterate(ctx, nil)
		if err != nil {
			return sdk.FormatInvariant(types.ModuleName, "fee-pool-shares-consistency",
				fmt.Sprintf("iterate shares: %v", err)), true
		}
		for ; shareIter.Valid(); shareIter.Next() {
			key, err := shareIter.Key()
			if err != nil {
				shareIter.Close()
				return sdk.FormatInvariant(types.ModuleName, "fee-pool-shares-consistency",
					fmt.Sprintf("shares key: %v", err)), true
			}
			val, err := shareIter.Value()
			if err != nil {
				shareIter.Close()
				return sdk.FormatInvariant(types.ModuleName, "fee-pool-shares-consistency",
					fmt.Sprintf("shares value: %v", err)), true
			}
			cid, denom := key.K1(), key.K2()
			if _, ok := sums[cid]; !ok {
				sums[cid] = map[string]math.Int{}
			}
			if cur, ok := sums[cid][denom]; ok {
				sums[cid][denom] = cur.Add(val)
			} else {
				sums[cid][denom] = val
			}
		}
		shareIter.Close()

		// Pass 2: compare against stored totals; also catch orphan totals.
		seen := map[uint64]map[string]bool{}
		totalIter, err := k.ConsumerFeePoolTotalShares.Iterate(ctx, nil)
		if err != nil {
			return sdk.FormatInvariant(types.ModuleName, "fee-pool-shares-consistency",
				fmt.Sprintf("iterate totals: %v", err)), true
		}
		for ; totalIter.Valid(); totalIter.Next() {
			key, err := totalIter.Key()
			if err != nil {
				totalIter.Close()
				return sdk.FormatInvariant(types.ModuleName, "fee-pool-shares-consistency",
					fmt.Sprintf("totals key: %v", err)), true
			}
			total, err := totalIter.Value()
			if err != nil {
				totalIter.Close()
				return sdk.FormatInvariant(types.ModuleName, "fee-pool-shares-consistency",
					fmt.Sprintf("totals value: %v", err)), true
			}
			cid, denom := key.K1(), key.K2()
			if _, ok := seen[cid]; !ok {
				seen[cid] = map[string]bool{}
			}
			seen[cid][denom] = true

			expected, ok := sums[cid][denom]
			if !ok {
				msgs = append(msgs, fmt.Sprintf(
					"total %s for (%d, %s) has no share records", total, cid, denom))
				continue
			}
			if !expected.Equal(total) {
				msgs = append(msgs, fmt.Sprintf(
					"sum(shares)=%s != total=%s for (%d, %s)", expected, total, cid, denom))
			}

			// Lazy-invalidation invariant: total > 0 implies pool balance > 0.
			if total.IsPositive() {
				bal := k.bankKeeper.GetBalance(ctx, k.GetConsumerFeePoolAddress(cid), denom)
				if !bal.Amount.IsPositive() {
					msgs = append(msgs, fmt.Sprintf(
						"total %s for (%d, %s) but pool balance is zero", total, cid, denom))
				}
			}
		}
		totalIter.Close()

		// Orphan shares (shares row with no totals row).
		for cid, byDenom := range sums {
			for denom := range byDenom {
				if !seen[cid][denom] {
					msgs = append(msgs, fmt.Sprintf(
						"shares for (%d, %s) without stored total", cid, denom))
				}
			}
		}

		// Orphan-balance invariant: every non-zero pool balance must have shares.
		// Walk FeePoolAddressToConsumerId so we only touch addresses we own.
		addrIter, err := k.FeePoolAddressToConsumerId.Iterate(ctx, nil)
		if err != nil {
			return sdk.FormatInvariant(types.ModuleName, "fee-pool-shares-consistency",
				fmt.Sprintf("iterate fee pool addrs: %v", err)), true
		}
		for ; addrIter.Valid(); addrIter.Next() {
			addr, err := addrIter.Key()
			if err != nil {
				addrIter.Close()
				return sdk.FormatInvariant(types.ModuleName, "fee-pool-shares-consistency",
					fmt.Sprintf("addr key: %v", err)), true
			}
			cid, err := addrIter.Value()
			if err != nil {
				addrIter.Close()
				return sdk.FormatInvariant(types.ModuleName, "fee-pool-shares-consistency",
					fmt.Sprintf("addr value: %v", err)), true
			}
			for _, coin := range k.bankKeeper.GetAllBalances(ctx, addr) {
				if !coin.Amount.IsPositive() {
					continue
				}
				if !seen[cid][coin.Denom] {
					msgs = append(msgs, fmt.Sprintf(
						"pool (%d, %s) holds %s with no shares", cid, coin.Denom, coin.Amount))
				}
			}
		}
		addrIter.Close()

		if len(msgs) > 0 {
			return sdk.FormatInvariant(types.ModuleName, "fee-pool-shares-consistency",
				strings.Join(msgs, "; ")), true
		}
		return "", false
	}
}
