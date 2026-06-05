package keeper

import (
	"fmt"
	"strings"

	types "github.com/allinbits/vaas/x/vaas/provider/types"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// RegisterInvariants registers all staking invariants
func RegisterInvariants(ir sdk.InvariantRegistry, k *Keeper) {
	ir.RegisterRoute(types.ModuleName, "max-provider-validators",
		MaxProviderConsensusValidatorsInvariant(k))

	ir.RegisterRoute(types.ModuleName, "staking-keeper-equivalence",
		StakingKeeperEquivalenceInvariant(*k))

	ir.RegisterRoute(types.ModuleName, "fee-pool-shares-consistency",
		FeePoolSharesConsistencyInvariant(*k))
}

// MaxProviderConsensusValidatorsInvariant checks that the number of provider consensus validators
// is less than or equal to the maximum number of provider consensus validators
func MaxProviderConsensusValidatorsInvariant(k *Keeper) sdk.Invariant {
	return func(ctx sdk.Context) (string, bool) {
		params := k.GetParams(ctx)
		maxProviderConsensusValidators := params.MaxProviderConsensusValidators

		consensusValidators, err := k.GetLastProviderConsensusValSet(ctx)
		if err != nil {
			panic(fmt.Errorf("failed to get last provider consensus validator set: %w", err))
		}
		if int64(len(consensusValidators)) > maxProviderConsensusValidators {
			return sdk.FormatInvariant(types.ModuleName, "max-provider-validators",
				fmt.Sprintf("number of provider consensus validators: %d, exceeds max: %d",
					len(consensusValidators), maxProviderConsensusValidators)), true
		}

		return "", false
	}
}

// StakingKeeperEquivalenceInvariant checks that *if* MaxProviderConsensusValidators == MaxValidators, then
// the staking keeper and the provider keeper
// return the same values for their common interface,
// i.e. the functions from staking_keeper_interface.go
func StakingKeeperEquivalenceInvariant(k Keeper) sdk.Invariant {
	return func(ctx sdk.Context) (string, bool) {
		maxProviderConsensusValidators := k.GetParams(ctx).MaxProviderConsensusValidators
		if maxProviderConsensusValidators == 0 {
			return sdk.FormatInvariant(types.ModuleName, "staking-keeper-equivalence",
				fmt.Sprintf("maxProviderConsensusVals is 0: %v", maxProviderConsensusValidators)), true
		}
		stakingKeeper := k.stakingKeeper

		maxValidators, err := stakingKeeper.MaxValidators(ctx)
		if err != nil {
			return sdk.FormatInvariant(types.ModuleName, "staking-keeper-equivalence",
				fmt.Sprintf("error getting max validators from staking keeper: %v", err)), true
		}

		if maxValidators != uint32(maxProviderConsensusValidators) {
			// the invariant should only check something if these two numbers are equal,
			// so skip in this case
			k.Logger(ctx).Info("skipping staking keeper equivalence invariant check because max validators is equal to max provider consensus validators")
			return sdk.FormatInvariant(types.ModuleName, "staking-keeper-equivalence",
				fmt.Sprintf("maxValidators: %v, maxProviderVals: %v", maxValidators, maxProviderConsensusValidators)), false
		}

		// check that the staking keeper and the provider keeper return the same values
		// for the common interface functions

		// Check IterateBondedValidatorsByPower
		providerBondedValidators := make([]stakingtypes.Validator, 0)
		err = k.IterateBondedValidatorsByPower(ctx, func(index int64, validator stakingtypes.ValidatorI) (stop bool) {
			providerBondedValidators = append(providerBondedValidators, validator.(stakingtypes.Validator))
			return false
		})
		if err != nil {
			return sdk.FormatInvariant(types.ModuleName, "staking-keeper-equivalence",
				fmt.Sprintf("error getting provider bonded validators: %v", err)), true
		}

		stakingBondedValidators := make([]stakingtypes.Validator, 0)
		err = stakingKeeper.IterateBondedValidatorsByPower(ctx, func(index int64, validator stakingtypes.ValidatorI) (stop bool) {
			stakingBondedValidators = append(stakingBondedValidators, validator.(stakingtypes.Validator))
			return false
		})
		if err != nil {
			return sdk.FormatInvariant(types.ModuleName, "staking-keeper-equivalence",
				fmt.Sprintf("error getting staking bonded validators: %v", err)), true
		}

		if len(providerBondedValidators) != len(stakingBondedValidators) {
			return sdk.FormatInvariant(types.ModuleName, "staking-keeper-equivalence",
				fmt.Sprintf("provider bonded validators: %v, staking bonded validators: %v",
					providerBondedValidators, stakingBondedValidators)), true
		}

		for i, providerVal := range providerBondedValidators {
			stakingVal := stakingBondedValidators[i]

			if !providerVal.Equal(&stakingVal) {
				return sdk.FormatInvariant(types.ModuleName, "staking-keeper-equivalence",
					fmt.Sprintf("provider validator: %v, staking validator: %v",
						providerVal, stakingVal)), true
			}
		}

		// Check TotalBondedTokens
		providerTotalBondedTokens, err := k.TotalBondedTokens(ctx)
		if err != nil {
			return sdk.FormatInvariant(types.ModuleName, "staking-keeper-equivalence",
				fmt.Sprintf("error getting provider total bonded tokens: %v", err)), true
		}

		stakingTotalBondedTokens, err := stakingKeeper.TotalBondedTokens(ctx)
		if err != nil {
			return sdk.FormatInvariant(types.ModuleName, "staking-keeper-equivalence",
				fmt.Sprintf("error getting staking total bonded tokens: %v", err)), true
		}

		if !providerTotalBondedTokens.Equal(stakingTotalBondedTokens) {
			return sdk.FormatInvariant(types.ModuleName, "staking-keeper-equivalence",
				fmt.Sprintf("provider total bonded tokens: %v, staking total bonded tokens: %v",
					providerTotalBondedTokens, stakingTotalBondedTokens)), true
		}

		// Check BondedRatio
		providerBondedRatio, err := k.BondedRatio(ctx)
		if err != nil {
			return sdk.FormatInvariant(types.ModuleName, "staking-keeper-equivalence",
				fmt.Sprintf("error getting provider bonded ratio: %v", err)), true
		}

		stakingBondedRatio, err := stakingKeeper.BondedRatio(ctx)
		if err != nil {
			return sdk.FormatInvariant(types.ModuleName, "staking-keeper-equivalence",
				fmt.Sprintf("error getting staking bonded ratio: %v", err)), true
		}

		if !providerBondedRatio.Equal(stakingBondedRatio) {
			return sdk.FormatInvariant(types.ModuleName, "staking-keeper-equivalence",
				fmt.Sprintf("provider bonded ratio: %v, staking bonded ratio: %v",
					providerBondedRatio, stakingBondedRatio)), true
		}

		return "", false
	}
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
