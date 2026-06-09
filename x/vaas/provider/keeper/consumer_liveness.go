package keeper

import (
	"context"
	"fmt"
	"time"

	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	"cosmossdk.io/collections"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// GraceFraction is the fraction of the provider unbonding period a consumer may
// stay unreachable (no successful VSC acknowledgement) before it is removed.
// Kept well below 1 so removal still happens inside the slashable-stake window.
const GraceFraction = "0.33"

// --- Liveness time ---

// SetConsumerLastLivenessTime records the block time of the last successful VSC
// acknowledgement for the given consumer.
func (k Keeper) SetConsumerLastLivenessTime(ctx context.Context, consumerId uint64, t time.Time) error {
	buf, err := t.MarshalBinary()
	if err != nil {
		return fmt.Errorf("failed to marshal liveness time for consumer id (%d): %w", consumerId, err)
	}
	return k.ConsumerLastLivenessTime.Set(ctx, consumerId, buf)
}

// GetConsumerLastLivenessTime returns the last liveness time and whether one is stored.
func (k Keeper) GetConsumerLastLivenessTime(ctx context.Context, consumerId uint64) (time.Time, bool) {
	buf, err := k.ConsumerLastLivenessTime.Get(ctx, consumerId)
	if err != nil {
		return time.Time{}, false
	}
	var t time.Time
	if err := t.UnmarshalBinary(buf); err != nil {
		return time.Time{}, false
	}
	return t, true
}

// DeleteConsumerLastLivenessTime removes the stored liveness time for the consumer.
func (k Keeper) DeleteConsumerLastLivenessTime(ctx context.Context, consumerId uint64) {
	if err := k.ConsumerLastLivenessTime.Remove(ctx, consumerId); err != nil {
		k.Logger(ctx).Error("failed to delete liveness time", "consumerId", consumerId, "err", err.Error())
	}
}

// --- Latest-sent VSC id ---

// SetConsumerLatestSentVscId records the vsc id of the most recently sent VSC packet.
func (k Keeper) SetConsumerLatestSentVscId(ctx context.Context, consumerId, vscId uint64) error {
	return k.ConsumerLatestSentVscId.Set(ctx, consumerId, vscId)
}

// GetConsumerLatestSentVscId returns the latest sent vsc id and whether one is stored.
func (k Keeper) GetConsumerLatestSentVscId(ctx context.Context, consumerId uint64) (uint64, bool) {
	vscId, err := k.ConsumerLatestSentVscId.Get(ctx, consumerId)
	if err != nil {
		return 0, false
	}
	return vscId, true
}

// DeleteConsumerLatestSentVscId removes the stored latest sent vsc id for the consumer.
func (k Keeper) DeleteConsumerLatestSentVscId(ctx context.Context, consumerId uint64) {
	if err := k.ConsumerLatestSentVscId.Remove(ctx, consumerId); err != nil {
		k.Logger(ctx).Error("failed to delete latest sent vsc id", "consumerId", consumerId, "err", err.Error())
	}
}

// --- Valset helpers parameterised over the target map ---

func (k Keeper) getValSetFrom(
	ctx context.Context,
	m collections.Map[collections.Pair[uint64, []byte], types.ConsensusValidator],
	consumerId uint64,
) ([]types.ConsensusValidator, error) {
	var validators []types.ConsensusValidator
	iter, err := m.Iterate(ctx, collections.NewPrefixedPairRange[uint64, []byte](consumerId))
	if err != nil {
		return validators, err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		val, err := iter.Value()
		if err != nil {
			return validators, err
		}
		validators = append(validators, val)
	}
	return validators, nil
}

func (k Keeper) clearValSetIn(
	ctx context.Context,
	m collections.Map[collections.Pair[uint64, []byte], types.ConsensusValidator],
	consumerId uint64,
) {
	iter, err := m.Iterate(ctx, collections.NewPrefixedPairRange[uint64, []byte](consumerId))
	if err != nil {
		return
	}
	defer iter.Close()

	var keysToDel []collections.Pair[uint64, []byte]
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			continue
		}
		keysToDel = append(keysToDel, key)
	}
	for _, key := range keysToDel {
		if err := m.Remove(ctx, key); err != nil {
			panic(fmt.Errorf("failed to delete consumer validator: %w", err))
		}
	}
}

func (k Keeper) replaceValSetIn(
	ctx context.Context,
	m collections.Map[collections.Pair[uint64, []byte], types.ConsensusValidator],
	consumerId uint64,
	nextValidators []types.ConsensusValidator,
) error {
	k.clearValSetIn(ctx, m, consumerId)
	for _, val := range nextValidators {
		if err := m.Set(ctx, collections.Join(consumerId, val.ProviderConsAddr), val); err != nil {
			return err
		}
	}
	return nil
}

// --- Acknowledged baseline valset ---

// GetAckedConsumerValSet returns the last acknowledged validator set used as the
// baseline for computing VSC diffs.
func (k Keeper) GetAckedConsumerValSet(ctx context.Context, consumerId uint64) ([]types.ConsensusValidator, error) {
	return k.getValSetFrom(ctx, k.AckedConsumerValidators, consumerId)
}

// SetAckedConsumerValSet replaces the acknowledged baseline validator set.
func (k Keeper) SetAckedConsumerValSet(ctx context.Context, consumerId uint64, vals []types.ConsensusValidator) error {
	return k.replaceValSetIn(ctx, k.AckedConsumerValidators, consumerId, vals)
}

// DeleteAckedConsumerValSet removes the acknowledged baseline validator set.
func (k Keeper) DeleteAckedConsumerValSet(ctx context.Context, consumerId uint64) {
	k.clearValSetIn(ctx, k.AckedConsumerValidators, consumerId)
}

// --- Latest-sent valset snapshot ---

// GetLatestSentConsumerValSet returns the validator set snapshot taken at the last
// successful VSC send.
func (k Keeper) GetLatestSentConsumerValSet(ctx context.Context, consumerId uint64) ([]types.ConsensusValidator, error) {
	return k.getValSetFrom(ctx, k.LatestSentConsumerValidators, consumerId)
}

// SetLatestSentConsumerValSet replaces the latest-sent validator set snapshot.
func (k Keeper) SetLatestSentConsumerValSet(ctx context.Context, consumerId uint64, vals []types.ConsensusValidator) error {
	return k.replaceValSetIn(ctx, k.LatestSentConsumerValidators, consumerId, vals)
}

// DeleteLatestSentConsumerValSet removes the latest-sent validator set snapshot.
func (k Keeper) DeleteLatestSentConsumerValSet(ctx context.Context, consumerId uint64) {
	k.clearValSetIn(ctx, k.LatestSentConsumerValidators, consumerId)
}

// --- Grace period & sweep ---

// LivenessGracePeriod returns how long a consumer may stay unreachable before it
// is removed. It is derived from the provider unbonding period so removal always
// happens while a leaving validator's stake is still slashable.
func (k Keeper) LivenessGracePeriod(ctx context.Context) (time.Duration, error) {
	unbonding, err := k.stakingKeeper.UnbondingTime(ctx)
	if err != nil {
		return 0, err
	}
	return vaastypes.CalculateTrustPeriod(unbonding, GraceFraction)
}

// shouldRemoveUnresponsiveConsumer reports whether the consumer has been without a
// successful VSC acknowledgement for longer than the grace period. When no liveness
// time is stored yet (e.g. consumers launched before this feature) it lazily seeds
// it with the current block time so existing consumers are not removed at upgrade.
func (k Keeper) shouldRemoveUnresponsiveConsumer(ctx sdk.Context, consumerId uint64) bool {
	last, found := k.GetConsumerLastLivenessTime(ctx, consumerId)
	if !found {
		if err := k.SetConsumerLastLivenessTime(ctx, consumerId, ctx.BlockTime()); err != nil {
			k.Logger(ctx).Error("failed to seed liveness time", "consumerId", consumerId, "err", err.Error())
		}
		return false
	}
	grace, err := k.LivenessGracePeriod(ctx)
	if err != nil {
		k.Logger(ctx).Error("failed to compute liveness grace period", "err", err.Error())
		return false
	}
	return ctx.BlockTime().Sub(last) > grace
}

// BeginBlockRemoveUnresponsiveConsumers stops and schedules removal for every
// launched consumer whose liveness grace period has elapsed. This is the single
// removal path for unreachable consumers: timeouts, error-acks and send failures
// only refrain from refreshing liveness, and a fully dead relayer fires no
// callback at all, so removal must be driven from BeginBlock rather than the IBC
// callbacks.
func (k Keeper) BeginBlockRemoveUnresponsiveConsumers(ctx sdk.Context) error {
	for _, consumerId := range k.GetAllLaunchedConsumerIds(ctx) {
		if !k.shouldRemoveUnresponsiveConsumer(ctx, consumerId) {
			continue
		}
		k.Logger(ctx).Info("removing unresponsive consumer (liveness grace exceeded)", "consumerId", consumerId)
		if err := k.StopAndPrepareForConsumerRemoval(ctx, consumerId); err != nil {
			k.Logger(ctx).Error("failed to stop unresponsive consumer", "consumerId", consumerId, "err", err.Error())
		}
	}
	return nil
}

// advanceAckedBaseline moves the acknowledged baseline forward when the consumer
// acknowledges the most recently sent VSC packet. Because every VSC diff is
// computed against the current baseline (a complete delta, not an increment), an
// ack for the latest-sent id proves the consumer now holds the snapshotted
// latest-sent set. Acks for older ids are ignored; the baseline stays put and the
// next epoch re-sends the delta against it.
func (k Keeper) advanceAckedBaseline(ctx sdk.Context, consumerId, ackedVscId uint64) {
	latestSentVscId, found := k.GetConsumerLatestSentVscId(ctx, consumerId)
	if !found || ackedVscId != latestSentVscId {
		return
	}
	latestSent, err := k.GetLatestSentConsumerValSet(ctx, consumerId)
	if err != nil {
		k.Logger(ctx).Error("failed to read latest-sent valset for baseline advance", "consumerId", consumerId, "err", err.Error())
		return
	}
	if err := k.SetAckedConsumerValSet(ctx, consumerId, latestSent); err != nil {
		k.Logger(ctx).Error("failed to advance acked baseline", "consumerId", consumerId, "err", err.Error())
	}
}

// recordLatestSentVSC snapshots the current consumer validator set and the given
// vsc id as the most recent successful send, so a later acknowledgement can
// advance the baseline to it.
func (k Keeper) recordLatestSentVSC(ctx context.Context, consumerId, vscId uint64) error {
	set, err := k.GetConsumerValSet(ctx, consumerId)
	if err != nil {
		return fmt.Errorf("reading consumer valset for latest-sent snapshot, consumerId(%d): %w", consumerId, err)
	}
	if err := k.SetLatestSentConsumerValSet(ctx, consumerId, set); err != nil {
		return fmt.Errorf("snapshotting latest-sent valset, consumerId(%d): %w", consumerId, err)
	}
	return k.SetConsumerLatestSentVscId(ctx, consumerId, vscId)
}
