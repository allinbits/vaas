package keeper

import (
	"context"
	"fmt"
	"time"

	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

// GetConsumerFeePoolAddress returns the deterministic provider-side fee pool
// account for a consumer. This is a plain account address used for fee funding,
// not a registered module account in the app's module-account permissions.
func (k Keeper) GetConsumerFeePoolAddress(consumerId uint64) sdk.AccAddress {
	return authtypes.NewModuleAddress(fmt.Sprintf("%s-consumer-fee-pool-%d", types.ModuleName, consumerId))
}

// MarkEpochDowntime records a validator's provider consensus address as
// having downtime evidence in the current epoch for the given consumer.
func (k Keeper) MarkEpochDowntime(ctx sdk.Context, consumerId uint64, providerConsAddr sdk.ConsAddress) {
	if err := k.EpochDowntime.Set(ctx, collections.Join(consumerId, providerConsAddr.Bytes()), true); err != nil {
		panic(fmt.Errorf("failed to mark epoch downtime for consumer %d, %x: %w", consumerId, providerConsAddr, err))
	}
}

// IsEpochDowntime returns true if the validator had downtime evidence this
// epoch on the given consumer.
func (k Keeper) IsEpochDowntime(ctx sdk.Context, consumerId uint64, providerConsAddr sdk.ConsAddress) bool {
	found, err := k.EpochDowntime.Has(ctx, collections.Join(consumerId, providerConsAddr.Bytes()))
	if err != nil {
		panic(fmt.Errorf("failed to check epoch downtime for consumer %d, %x: %w", consumerId, providerConsAddr, err))
	}
	return found
}

// ClearEpochDowntime removes all downtime records for the current epoch.
func (k Keeper) ClearEpochDowntime(ctx sdk.Context) {
	if err := k.EpochDowntime.Clear(ctx, nil); err != nil {
		panic(fmt.Errorf("failed to clear epoch downtime: %w", err))
	}
}

// SetEpochShareRecord records the per-validator fee share that a consumer's
// distribution run at distributedAt actually paid out (zero when the
// consumer's pool was underfunded and the run was skipped). Recorded exactly
// once per launched consumer per distribution run, regardless of validator
// eligibility, so later infraction pricing can resolve what the share was at
// any point in time.
func (k Keeper) SetEpochShareRecord(ctx sdk.Context, consumerId uint64, distributedAt time.Time, share math.Int) {
	key := collections.Join(consumerId, distributedAt.UnixNano())
	if err := k.EpochShareRecords.Set(ctx, key, share); err != nil {
		panic(fmt.Errorf("failed to set epoch share record for consumer %d: %w", consumerId, err))
	}
}

// ResolveEpochShare returns the per-validator share paid out by the
// distribution run that covered consumer time t: the earliest recorded run
// with distributedAt >= t. found=false means t falls in the current,
// not-yet-distributed epoch (or the consumer has no records at all).
func (k Keeper) ResolveEpochShare(ctx sdk.Context, consumerId uint64, t time.Time) (share math.Int, found bool) {
	rng := collections.NewPrefixedPairRange[uint64, int64](consumerId).StartInclusive(t.UnixNano())
	iter, err := k.EpochShareRecords.Iterate(ctx, rng)
	if err != nil {
		return math.Int{}, false
	}
	defer iter.Close()

	if !iter.Valid() {
		return math.Int{}, false
	}
	share, err = iter.Value()
	if err != nil {
		return math.Int{}, false
	}
	return share, true
}

// PruneEpochShareRecords removes epoch share records (across all consumers)
// whose distribution time is older than olderThan. Callers wire the
// retention horizon so that records are kept at least as long as downtime
// evidence and its challenge window can reference them.
func (k Keeper) PruneEpochShareRecords(ctx sdk.Context, olderThan time.Time) {
	iter, err := k.EpochShareRecords.Iterate(ctx, nil)
	if err != nil {
		panic(fmt.Errorf("failed to iterate epoch share records: %w", err))
	}
	defer iter.Close()

	cutoff := olderThan.UnixNano()
	var keysToDel []collections.Pair[uint64, int64]
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			continue
		}
		if key.K2() < cutoff {
			keysToDel = append(keysToDel, key)
		}
	}

	for _, key := range keysToDel {
		if err := k.EpochShareRecords.Remove(ctx, key); err != nil {
			panic(fmt.Errorf("failed to prune epoch share record: %w", err))
		}
	}
}

// epochShareForConsumer resolves the per-consumer epoch fee (the per-consumer
// override multiplied by blocks_per_epoch if one is set, else the default
// epoch fee) and divides it evenly among numBonded. Returns both the epoch
// fee, needed by DistributeConsumerFees for its balance/denom bookkeeping,
// and the resulting per-validator share. Shared by DistributeConsumerFees
// (recording the share actually paid out this epoch) and liveEpochShare
// (pricing downtime evidence whose window falls in the current,
// not-yet-distributed epoch), so both paths agree on exactly how a share is
// derived.
func (k Keeper) epochShareForConsumer(ctx context.Context, consumerId uint64, defaultFeePerEpoch, defaultFeePerBlock sdk.Coin, numBonded math.Int) (consumerFeePerEpoch sdk.Coin, share math.Int) {
	consumerFeePerEpoch, _ = k.effectiveFeesPerEpoch(ctx, consumerId, defaultFeePerEpoch, defaultFeePerBlock)
	share = consumerFeePerEpoch.Amount.Quo(numBonded)
	return consumerFeePerEpoch, share
}

// liveEpochShare computes the per-validator epoch fee share for consumerId
// using the parameters currently in effect. Used to price downtime evidence
// whose window ended after the most recent recorded EpochShareRecord (i.e.
// within the current, not-yet-distributed epoch), mirroring exactly what
// DistributeConsumerFees would record were it to run right now.
func (k Keeper) liveEpochShare(ctx sdk.Context, consumerId uint64) (math.Int, error) {
	defaultFeePerBlock := k.GetFeesPerBlock(ctx)
	blocksPerEpoch := k.GetBlocksPerEpoch(ctx)
	defaultFeePerEpoch := sdk.NewCoin(defaultFeePerBlock.Denom, defaultFeePerBlock.Amount.MulRaw(blocksPerEpoch))

	bonded, err := k.stakingKeeper.GetBondedValidatorsByPower(ctx)
	if err != nil {
		return math.Int{}, fmt.Errorf("failed to get bonded validators: %w", err)
	}
	if len(bonded) == 0 {
		return math.ZeroInt(), nil
	}
	numBonded := math.NewInt(int64(len(bonded)))

	_, share := k.epochShareForConsumer(ctx, consumerId, defaultFeePerEpoch, defaultFeePerBlock, numBonded)
	return share, nil
}

// ResolveDowntimeSlashTokens prices a downtime slash at receipt time. P is
// the per-validator epoch fee share in effect when the evidence window ended
// (resolved via ResolveEpochShare, falling back to liveEpochShare when the
// window falls in the current, not-yet-distributed epoch); M is the fraction
// of the window that was missed; C is the photon-per-bond-token conversion
// rate. The result is P * M / C, truncated to an integer token amount.
func (k Keeper) ResolveDowntimeSlashTokens(ctx sdk.Context, consumerId uint64, packet vaastypes.EvidencePacketData, windowEndTime time.Time) (math.Int, error) {
	share, found := k.ResolveEpochShare(ctx, consumerId, windowEndTime)
	if !found {
		var err error
		share, err = k.liveEpochShare(ctx, consumerId)
		if err != nil {
			return math.Int{}, err
		}
	}

	missedFraction := math.LegacyNewDec(packet.MissedCount()).QuoInt64(packet.Span())

	conversionRate, err := k.photonKeeper.ConversionRate(ctx)
	if err != nil {
		return math.Int{}, fmt.Errorf("resolving photon conversion rate: %w", err)
	}
	if conversionRate.IsZero() {
		return math.Int{}, fmt.Errorf("photon conversion rate is zero")
	}

	return share.ToLegacyDec().Mul(missedFraction).Quo(conversionRate).TruncateInt(), nil
}

// DistributeConsumerFees collects fees from each launched consumer's fee pool
// and distributes them directly to bonded validators in a single bank
// InputOutputCoins call per consumer. The bonded validator set is queried once;
// each consumer pays fees_per_epoch (fees_per_block * blocks_per_epoch), split
// equally as share = fees_per_epoch / num_bonded. Validators flagged with
// downtime are excluded from the outputs — only the eligible validators'
// shares (share * num_eligible) are drawn from the consumer pool, so the
// withheld shares remain available for the consumer. This ensures the
// consumer never pays for validation work that did not happen, and avoids
// incentivizing validators to DOS competitors (the other validators' shares
// do not increase when someone is excluded).
//
// If a consumer's pool balance is below fees_per_epoch, the consumer is marked
// as in debt and skipped entirely (no partial distribution).
func (k Keeper) DistributeConsumerFees(ctx sdk.Context) error {
	defaultFeePerBlock := k.GetFeesPerBlock(ctx)
	blocksPerEpoch := k.GetBlocksPerEpoch(ctx)
	defaultFeePerEpoch := sdk.NewCoin(defaultFeePerBlock.Denom, defaultFeePerBlock.Amount.MulRaw(blocksPerEpoch))

	bonded, err := k.stakingKeeper.GetBondedValidatorsByPower(ctx)
	if err != nil {
		return fmt.Errorf("failed to get bonded validators: %w", err)
	}
	if len(bonded) == 0 {
		return nil
	}
	numBonded := math.NewInt(int64(len(bonded)))

	// Precompute consensus and account addresses for all bonded validators.
	type bondedVal struct {
		consAddr sdk.ConsAddress
		accAddr  sdk.AccAddress
	}
	bondedVals := make([]bondedVal, len(bonded))
	for i, val := range bonded {
		consAddr, err := val.GetConsAddr()
		if err != nil {
			return fmt.Errorf("failed to get consensus address for validator %s: %w", val.GetOperator(), err)
		}
		valAddr, err := k.stakingKeeper.ValidatorAddressCodec().StringToBytes(val.GetOperator())
		if err != nil {
			return fmt.Errorf("failed to parse validator address %s: %w", val.GetOperator(), err)
		}
		bondedVals[i] = bondedVal{consAddr: consAddr, accAddr: valAddr}
	}

	consumerIds := k.GetAllActiveConsumerIds(ctx)
	for _, consumerId := range consumerIds {
		if k.GetConsumerPhase(ctx, consumerId) != types.CONSUMER_PHASE_LAUNCHED {
			continue
		}

		// consumerFeePerEpoch and share are computed once per launched
		// consumer per distribution run and recorded via SetEpochShareRecord
		// exactly once on every path: zero when nothing was paid out (debt-skip
		// or failed transfer), the computed per-validator share otherwise. This
		// ensures one record per consumer per run for later infraction-time share
		// resolution.
		consumerFeePerEpoch, share := k.epochShareForConsumer(ctx, consumerId, defaultFeePerEpoch, defaultFeePerBlock, numBonded)

		// Filter eligible validators for this consumer.
		var eligibleAddrs []sdk.AccAddress
		for _, bv := range bondedVals {
			if k.IsEpochDowntime(ctx, consumerId, bv.consAddr) {
				continue
			}
			eligibleAddrs = append(eligibleAddrs, bv.accAddr)
		}
		if len(eligibleAddrs) == 0 {
			k.SetEpochShareRecord(ctx, consumerId, ctx.BlockTime(), share)
			continue
		}

		if share.IsZero() {
			k.SetEpochShareRecord(ctx, consumerId, ctx.BlockTime(), share)
			continue
		}
		shareCoin := sdk.NewCoin(consumerFeePerEpoch.Denom, share)
		shareCoins := sdk.NewCoins(shareCoin)
		consumerFeePoolAddr := k.GetConsumerFeePoolAddress(consumerId)

		// Check the pool can cover the full epoch fee before paying any
		// validator. This avoids unfair partial distribution.
		balance := k.bankKeeper.GetBalance(ctx, consumerFeePoolAddr, consumerFeePerEpoch.Denom)
		if balance.Amount.LT(consumerFeePerEpoch.Amount) {
			k.UpdateConsumerDebtStatus(ctx, consumerId, true)
			k.SetEpochShareRecord(ctx, consumerId, ctx.BlockTime(), math.ZeroInt())
			k.Logger(ctx).Debug("consumer fee pool underfunded; skipping distribution",
				"consumerId", consumerId,
				"balance", balance.String(),
				"required", consumerFeePerEpoch.String(),
			)
			continue
		}

		// Single InputOutputCoins: consumer pool → all eligible validators.
		// Only share*num_eligible is drawn; excluded validators' shares stay
		// in the consumer pool.
		totalOut := share.MulRaw(int64(len(eligibleAddrs)))
		outputs := make([]banktypes.Output, len(eligibleAddrs))
		for i, addr := range eligibleAddrs {
			outputs[i] = banktypes.Output{Address: addr.String(), Coins: shareCoins}
		}
		input := banktypes.Input{
			Address: consumerFeePoolAddr.String(),
			Coins:   sdk.NewCoins(sdk.NewCoin(consumerFeePerEpoch.Denom, totalOut)),
		}
		// Run in a cached context so a mid-distribution error (e.g. a send
		// restriction on one output) rolls back the entire operation instead
		// of leaving some validators paid and others not.
		cachedCtx, write := ctx.CacheContext()
		if err := k.bankKeeper.InputOutputCoins(cachedCtx, input, outputs); err != nil {
			k.Logger(ctx).Error("failed to distribute consumer fees",
				"consumerId", consumerId,
				"err", err,
			)
			k.SetEpochShareRecord(ctx, consumerId, ctx.BlockTime(), math.ZeroInt())
			continue
		}
		write()
		k.UpdateConsumerDebtStatus(ctx, consumerId, false)
		k.SetEpochShareRecord(ctx, consumerId, ctx.BlockTime(), share)
		k.Logger(ctx).Debug("distributed consumer fees to validators",
			"consumerId", consumerId,
			"sharePerValidator", shareCoins.String(),
			"totalDistributed", sdk.NewCoins(sdk.NewCoin(consumerFeePerEpoch.Denom, totalOut)).String(),
		)
	}

	return nil
}
