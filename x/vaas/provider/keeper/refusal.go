package keeper

import (
	"errors"
	"fmt"

	"github.com/allinbits/vaas/x/vaas/provider/types"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// RecordConsumerRefusal records (refused=true) or withdraws (refused=false) a
// validator's public refusal to validate a consumer. The signal is an
// aggregation device toward the threshold pause (EvaluateConsumerRefusals)
// and a public on-chain stance; it does not by itself excuse the validator
// from the consumer's downtime evidence. Signals survive a pause: withdrawing
// one is the validator's own act, and a governance resume is refused while
// the coalition still stands at the threshold (see ResumeConsumerChain).
func (k Keeper) RecordConsumerRefusal(ctx sdk.Context, consumerId uint64, valAddr sdk.ValAddress, refused bool) error {
	key := collections.Join(consumerId, valAddr.Bytes())
	if !refused {
		if err := k.ConsumerRefusals.Remove(ctx, key); err != nil {
			return fmt.Errorf("withdrawing refusal for consumer %d: %w", consumerId, err)
		}
		return nil
	}
	if err := k.ConsumerRefusals.Set(ctx, key); err != nil {
		return fmt.Errorf("recording refusal for consumer %d: %w", consumerId, err)
	}
	return nil
}

// HasConsumerRefusal reports whether the validator currently refuses the
// consumer.
func (k Keeper) HasConsumerRefusal(ctx sdk.Context, consumerId uint64, valAddr sdk.ValAddress) bool {
	has, err := k.ConsumerRefusals.Has(ctx, collections.Join(consumerId, valAddr.Bytes()))
	if err != nil {
		return false
	}
	return has
}

// GetConsumerRefusals returns the operator addresses currently refusing the
// consumer.
func (k Keeper) GetConsumerRefusals(ctx sdk.Context, consumerId uint64) ([]sdk.ValAddress, error) {
	var addrs []sdk.ValAddress
	rng := collections.NewPrefixedPairRange[uint64, []byte](consumerId)
	if err := k.ConsumerRefusals.Walk(ctx, rng, func(key collections.Pair[uint64, []byte]) (bool, error) {
		addrs = append(addrs, sdk.ValAddress(key.K2()))
		return false, nil
	}); err != nil {
		return nil, err
	}
	return addrs, nil
}

// DeleteConsumerRefusals removes every refusal recorded for the consumer;
// called when the consumer is deleted.
func (k Keeper) DeleteConsumerRefusals(ctx sdk.Context, consumerId uint64) error {
	rng := collections.NewPrefixedPairRange[uint64, []byte](consumerId)
	return k.ConsumerRefusals.Clear(ctx, rng)
}

// RefusedPower sums the bonded power behind the consumer's refusal signals,
// as last recorded by x/staking, and returns it with the total bonded power.
// Signals from validators that have since lost all bonded power contribute
// zero; power is read at evaluation time, never at signal time.
func (k Keeper) RefusedPower(ctx sdk.Context, consumerId uint64) (refused int64, total int64, err error) {
	addrs, err := k.GetConsumerRefusals(ctx, consumerId)
	if err != nil {
		return 0, 0, err
	}
	refused, total, _, err = k.refusedPowerOf(ctx, addrs)
	return refused, total, err
}

// refusedPowerOf sums the last recorded power of the given validators and
// returns it with the total bonded power, plus the validators that
// contributed nothing.
func (k Keeper) refusedPowerOf(ctx sdk.Context, addrs []sdk.ValAddress) (refused int64, total int64, powerless []sdk.ValAddress, err error) {
	for _, addr := range addrs {
		// x/staking reports zero, not an error, for a validator outside the
		// bonded set; an error here is a store failure.
		power, err := k.stakingKeeper.GetLastValidatorPower(ctx, addr)
		if err != nil {
			return 0, 0, nil, fmt.Errorf("reading last power of %s: %w", addr, err)
		}
		if power == 0 {
			powerless = append(powerless, addr)
		}
		refused += power
	}
	totalPower, err := k.stakingKeeper.GetLastTotalPower(ctx)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("reading total bonded power: %w", err)
	}
	if !totalPower.IsInt64() {
		return 0, 0, nil, fmt.Errorf("total bonded power %s exceeds int64", totalPower)
	}
	return refused, totalPower.Int64(), powerless, nil
}

// refusalStanding is a consumer's refused share of bonded power measured
// against the RefusalPauseThreshold param, with the signals that carried no
// power.
type refusalStanding struct {
	refused, total      int64
	fraction, threshold math.LegacyDec
	powerless           []sdk.ValAddress
}

// atThreshold reports whether the refused share has reached the pause
// threshold; nothing bonded means nothing refused.
func (s refusalStanding) atThreshold() bool {
	return s.total > 0 && !s.fraction.LT(s.threshold)
}

// consumerRefusalStanding measures the consumer's refusal signals against the
// pause threshold.
func (k Keeper) consumerRefusalStanding(ctx sdk.Context, consumerId uint64) (refusalStanding, error) {
	threshold, err := math.LegacyNewDecFromStr(k.GetParams(ctx).RefusalPauseThreshold)
	if err != nil {
		return refusalStanding{}, fmt.Errorf("invalid refusal pause threshold param: %w", err)
	}
	addrs, err := k.GetConsumerRefusals(ctx, consumerId)
	if err != nil {
		return refusalStanding{}, err
	}
	standing := refusalStanding{fraction: math.LegacyZeroDec(), threshold: threshold}
	if len(addrs) == 0 {
		// Nobody refuses: no power to weigh.
		return standing, nil
	}
	refused, total, powerless, err := k.refusedPowerOf(ctx, addrs)
	if err != nil {
		return refusalStanding{}, err
	}
	standing.refused, standing.total, standing.powerless = refused, total, powerless
	if total > 0 {
		standing.fraction = math.LegacyNewDec(refused).Quo(math.LegacyNewDec(total))
	}
	return standing, nil
}

// EvaluateConsumerRefusals pauses every launched consumer whose refused share
// of bonded power has reached the RefusalPauseThreshold param. It runs every
// EndBlock so a threshold crossed by delegation drift alone, with no new
// signal, still triggers. A refusing coalition of that size halts the
// consumer's consensus physically anyway; the pause turns the implicit halt
// into an explicit, attributable one with the standard PAUSED lifecycle
// (MaxPauseDuration auto-stop, governance resume once the coalition has
// withdrawn). Consumers are visited in key order: two pausing in one block
// share an auto-stop bucket, whose contents must not depend on iteration
// order.
func (k Keeper) EvaluateConsumerRefusals(ctx sdk.Context) {
	var consumerIds []uint64
	if err := k.ConsumerRefusals.Walk(ctx, nil, func(key collections.Pair[uint64, []byte]) (bool, error) {
		if n := len(consumerIds); n == 0 || consumerIds[n-1] != key.K1() {
			consumerIds = append(consumerIds, key.K1())
		}
		return false, nil
	}); err != nil {
		k.Logger(ctx).Error("failed to iterate consumer refusals", "error", err)
		return
	}

	for _, consumerId := range consumerIds {
		if k.GetConsumerPhase(ctx, consumerId) != types.CONSUMER_PHASE_LAUNCHED {
			continue
		}
		standing, err := k.consumerRefusalStanding(ctx, consumerId)
		if err != nil {
			k.Logger(ctx).Error("failed to evaluate consumer refusals",
				"consumerId", consumerId, "error", err)
			continue
		}
		k.pruneRefusalsOfGoneValidators(ctx, consumerId, standing.powerless)
		if !standing.atThreshold() {
			continue
		}
		if err := k.PauseConsumerChain(ctx, consumerId); err != nil {
			k.Logger(ctx).Error("failed to pause consumer at refusal threshold",
				"consumerId", consumerId, "error", err)
			continue
		}
		ctx.EventManager().EmitEvent(sdk.NewEvent(
			types.EventTypeConsumerRefusalThresholdReached,
			sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
			sdk.NewAttribute(types.AttributeConsumerId, fmt.Sprintf("%d", consumerId)),
			sdk.NewAttribute(types.AttributeRefusedPower, fmt.Sprintf("%d", standing.refused)),
			sdk.NewAttribute(types.AttributeTotalPower, fmt.Sprintf("%d", standing.total)),
			sdk.NewAttribute(types.AttributeRefusalThreshold, standing.threshold.String()),
		))
		k.Logger(ctx).Info("consumer paused: refusal threshold reached",
			"consumerId", consumerId,
			"refusedPower", standing.refused,
			"totalPower", standing.total,
			"threshold", standing.threshold.String(),
		)
	}
}

// pruneRefusalsOfGoneValidators drops the signals of powerless refusers that
// x/staking no longer knows at all: a validator removed after unbonding to
// zero shares cannot withdraw its own signal, and a validator created later
// under the same operator would inherit a stance it never took. Powerless
// but existing validators (jailed, out of the set) keep theirs.
func (k Keeper) pruneRefusalsOfGoneValidators(ctx sdk.Context, consumerId uint64, powerless []sdk.ValAddress) {
	for _, addr := range powerless {
		if _, err := k.stakingKeeper.GetValidator(ctx, addr); err == nil || !errors.Is(err, stakingtypes.ErrNoValidatorFound) {
			continue
		}
		if err := k.ConsumerRefusals.Remove(ctx, collections.Join(consumerId, addr.Bytes())); err != nil {
			k.Logger(ctx).Error("failed to prune the refusal of a removed validator",
				"consumerId", consumerId, "validator", addr.String(), "error", err)
		}
	}
}
