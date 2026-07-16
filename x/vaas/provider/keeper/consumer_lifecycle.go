package keeper

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	abci "github.com/cometbft/cometbft/abci/types"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	commitmenttypes "github.com/cosmos/ibc-go/v10/modules/core/23-commitment/types"
	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// PrepareConsumerForLaunch prepares to move the launch of a consumer chain from the previous spawn time to spawn time.
// Previous spawn time can correspond to its zero value if the validator was not previously set for launch.
func (k Keeper) PrepareConsumerForLaunch(ctx sdk.Context, consumerId uint64, previousSpawnTime, spawnTime time.Time) error {
	if !previousSpawnTime.IsZero() {
		// if this is not the first initialization and hence `previousSpawnTime` does not contain the zero value of `Time`
		// remove the consumer id from the previous spawn time
		err := k.RemoveConsumerToBeLaunched(ctx, consumerId, previousSpawnTime)
		if err != nil {
			return err
		}
	}
	return k.AppendConsumerToBeLaunched(ctx, consumerId, spawnTime)
}

// InitializeConsumer tries to move a consumer with `consumerId` to the initialized phase.
// If successful, it returns the spawn time and true.
func (k Keeper) InitializeConsumer(ctx sdk.Context, consumerId uint64) (time.Time, bool) {
	// a chain needs to be in the registered or initialized phase
	phase := k.GetConsumerPhase(ctx, consumerId)
	if phase != types.CONSUMER_PHASE_REGISTERED && phase != types.CONSUMER_PHASE_INITIALIZED {
		return time.Time{}, false
	}

	initializationParameters, err := k.GetConsumerInitializationParameters(ctx, consumerId)
	if err != nil {
		return time.Time{}, false
	}

	// the spawn time needs to be positive
	if initializationParameters.SpawnTime.IsZero() {
		return time.Time{}, false
	}

	k.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_INITIALIZED)

	return initializationParameters.SpawnTime, true
}

// BeginBlockLaunchConsumers launches initialized consumers chains for which the spawn time has passed
func (k Keeper) BeginBlockLaunchConsumers(ctx sdk.Context) error {
	bondedValidators := []stakingtypes.Validator{}

	consumerIds, err := k.ConsumeIdsFromTimeQueue(
		ctx,
		k.SpawnTimeToConsumerIds,
		k.GetConsumersToBeLaunched,
		k.DeleteAllConsumersToBeLaunched,
		k.AppendConsumerToBeLaunched,
		200,
	)
	if err != nil {
		return errorsmod.Wrapf(vaastypes.ErrInvalidConsumerState, "getting consumers ready to laumch: %s", err.Error())
	}
	if len(consumerIds) > 0 {
		// get the bonded validators from the staking module
		bondedValidators, err = k.GetLastBondedValidators(ctx)
		if err != nil {
			return fmt.Errorf("getting last bonded validators: %w", err)
		}
	}

	for _, consumerId := range consumerIds {
		cachedCtx, writeFn := ctx.CacheContext()
		err = k.LaunchConsumer(cachedCtx, bondedValidators, consumerId)
		if err != nil {
			ctx.Logger().Error("could not launch chain",
				"consumerId", consumerId,
				"error", err)

			// reset spawn time to zero so that owner can try again later
			initializationRecord, err := k.GetConsumerInitializationParameters(ctx, consumerId)
			if err != nil {
				return errorsmod.Wrapf(vaastypes.ErrInvalidConsumerState,
					"getting initialization parameters, consumerId(%d): %s", consumerId, err.Error())
			}
			initializationRecord.SpawnTime = time.Time{}
			err = k.SetConsumerInitializationParameters(ctx, consumerId, initializationRecord)
			if err != nil {
				return fmt.Errorf("setting consumer initialization parameters, consumerId(%d): %w", consumerId, err)
			}
			// also set the phase to registered
			k.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_REGISTERED)

			continue
		}

		writeFn()
	}
	return nil
}

// ConsumeIdsFromTimeQueue returns from a time queue the consumer ids for which the associated time passed.
// The number of ids return is limited to 'limit'. The ids returned are removed from the time queue.
func (k Keeper) ConsumeIdsFromTimeQueue(
	ctx sdk.Context,
	timeQueue collections.Map[[]byte, types.ConsumerIds],
	getIds func(context.Context, time.Time) (types.ConsumerIds, error),
	deleteAllIds func(context.Context, time.Time),
	appendId func(context.Context, uint64, time.Time) error,
	limit int,
) ([]uint64, error) {
	result := []uint64{}
	nextTime := []uint64{}
	timestampsToDelete := []time.Time{}

	iter, err := timeQueue.Iterate(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("iterating time queue: %w", err)
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		if len(result) >= limit {
			break
		}

		kv, err := iter.KeyValue()
		if err != nil {
			return result, fmt.Errorf("getting key-value from iterator: %w", err)
		}

		ts, err := bytesToTime(kv.Key)
		if err != nil {
			return result, fmt.Errorf("parsing time from key: %w", err)
		}
		if ts.After(ctx.BlockTime()) {
			break
		}

		consumerIds, err := getIds(ctx, ts)
		if err != nil {
			return result,
				fmt.Errorf("getting consumers ids, ts(%s): %w", ts.String(), err)
		}

		timestampsToDelete = append(timestampsToDelete, ts)

		availableSlots := limit - len(result)
		if availableSlots >= len(consumerIds.Ids) {
			// consumer all the ids
			result = append(result, consumerIds.Ids...)
		} else {
			// consume only availableSlots
			result = append(result, consumerIds.Ids[:availableSlots]...)
			// and leave the others for next time
			nextTime = consumerIds.Ids[availableSlots:]
			break
		}
	}

	// remove consumers to prevent handling them twice
	for i, ts := range timestampsToDelete {
		deleteAllIds(ctx, ts)
		if i == len(timestampsToDelete)-1 {
			// for the last ts consumed, store back the ids for later
			for _, consumerId := range nextTime {
				err := appendId(ctx, consumerId, ts)
				if err != nil {
					return result,
						fmt.Errorf("failed to append consumer id, consumerId(%d), ts(%s): %w",
							consumerId, ts.String(), err)
				}
			}
		}
	}

	return result, nil
}

// HasActiveConsumerValidator checks whether at least one active validator is opted in to chain with `consumerId`
func (k Keeper) HasActiveConsumerValidator(ctx sdk.Context, consumerId uint64, activeValidators []stakingtypes.Validator) (bool, error) {
	currentValidatorSet, err := k.GetConsumerValSet(ctx, consumerId)
	if err != nil {
		return false, fmt.Errorf("getting consumer validator set of chain with consumerId (%d): %w", consumerId, err)
	}

	isActiveValidator := make(map[string]bool)
	for _, val := range activeValidators {
		consAddr, err := val.GetConsAddr()
		if err != nil {
			return false, fmt.Errorf("getting consensus address of validator (%+v), consumerId (%d): %w", val, consumerId, err)
		}
		providerConsAddr := types.NewProviderConsAddress(consAddr)
		isActiveValidator[providerConsAddr.String()] = true
	}

	for _, val := range currentValidatorSet {
		providerConsAddr := types.NewProviderConsAddress(val.ProviderConsAddr)
		if isActiveValidator[providerConsAddr.String()] {
			return true, nil
		}
	}

	return false, nil
}

// LaunchConsumer launches the chain with the provided consumer id by creating the consumer genesis file.
// The IBC client is not created here; it is discovered later when the relayer creates one.
func (k Keeper) LaunchConsumer(
	ctx sdk.Context,
	bondedValidators []stakingtypes.Validator,
	consumerId uint64,
) error {
	initializationRecord, err := k.GetConsumerInitializationParameters(ctx, consumerId)
	if err != nil {
		return fmt.Errorf("getting initialization parameters, consumerId(%d): %w", consumerId, err)
	}

	// compute consumer initial validator set (all validators validate all consumers)
	initialValUpdates, err := k.ComputeConsumerNextValSet(ctx, bondedValidators, consumerId, []types.ConsensusValidator{}, false)
	if err != nil {
		return fmt.Errorf("computing consumer next validator set, consumerId(%d): %w", consumerId, err)
	}

	if len(initialValUpdates) == 0 {
		return fmt.Errorf("cannot launch consumer with no consumer validator, consumerId(%d)", consumerId)
	}

	// create consumer genesis
	genesisState, err := k.MakeConsumerGenesis(ctx, consumerId, initialValUpdates)
	if err != nil {
		return fmt.Errorf("creating consumer genesis state, consumerId(%d): %w", consumerId, err)
	}
	err = k.SetConsumerGenesis(ctx, consumerId, genesisState)
	if err != nil {
		return fmt.Errorf("setting consumer genesis state, consumerId(%d): %w", consumerId, err)
	}

	k.SetEquivocationEvidenceMinHeight(ctx, consumerId, initializationRecord.InitialHeight.RevisionHeight)

	k.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_LAUNCHED)
	if err := k.SetConsumerLastAckTime(ctx, consumerId, ctx.BlockTime()); err != nil {
		return err
	}

	k.Logger(ctx).Info("consumer successfully launched",
		"consumerId", consumerId,
		"valset size", len(initialValUpdates),
	)

	return nil
}

// MakeConsumerGenesis returns the created consumer genesis state for consumer chain `consumerId`,
// as well as the validator hash of the initial validator set of the consumer chain
func (k Keeper) MakeConsumerGenesis(
	ctx sdk.Context,
	consumerId uint64,
	initialValidatorUpdates []abci.ValidatorUpdate,
) (gen vaastypes.ConsumerGenesisState, err error) {
	initializationRecord, err := k.GetConsumerInitializationParameters(ctx, consumerId)
	if err != nil {
		return gen, errorsmod.Wrapf(vaastypes.ErrInvalidConsumerState,
			"getting initialization parameters, consumerId(%d): %s", consumerId, err.Error())
	}
	// Create consumer genesis params
	consumerGenesisParams := vaastypes.NewConsumerParams(
		true,
		initializationRecord.VaasTimeoutPeriod,
		initializationRecord.HistoricalEntries,
		initializationRecord.UnbondingPeriod,
		initializationRecord.SafeModeThreshold,
	)
	// Downtime detection params are provider-owned: seed genesis with the
	// current values so the consumer starts in sync; later changes are
	// distributed via VSC packets.
	downtimeParams := k.CurrentDowntimeParams(ctx)
	consumerGenesisParams.SignedBlocksWindow = downtimeParams.SignedBlocksWindow
	consumerGenesisParams.MinSignedPerWindow = downtimeParams.MinSignedPerWindow

	providerUnbondingPeriod, err := k.stakingKeeper.UnbondingTime(ctx)
	if err != nil {
		return gen, errorsmod.Wrapf(types.ErrNoUnbondingTime, "unbonding time not found: %s", err)
	}
	height := clienttypes.GetSelfHeight(ctx)

	clientState := k.GetTemplateClient(ctx)
	clientState.ChainId = ctx.ChainID()
	clientState.LatestHeight = height
	trustPeriod, err := vaastypes.CalculateTrustPeriod(providerUnbondingPeriod, k.GetTrustingPeriodFraction(ctx))
	if err != nil {
		return gen, errorsmod.Wrapf(sdkerrors.ErrInvalidHeight, "error %s calculating trusting_period for: %s", err, height)
	}
	clientState.TrustingPeriod = trustPeriod
	clientState.UnbondingPeriod = providerUnbondingPeriod

	consState, err := k.getSelfConsensusState(ctx, height)
	if err != nil {
		return gen, errorsmod.Wrapf(clienttypes.ErrConsensusStateNotFound, "error %s getting self consensus state for: %s", err, height)
	}

	gen = *vaastypes.NewInitialConsumerGenesisState(
		clientState,
		consState,
		initialValidatorUpdates,
		false,
		consumerGenesisParams,
	)

	return gen, nil
}

// This is copied from the client keeper in ibc v8, since this function was removed in ibc v9
func (k Keeper) getSelfConsensusState(ctx sdk.Context, height clienttypes.Height) (*ibctmtypes.ConsensusState, error) {
	// check that height revision matches chainID revision
	revision := clienttypes.ParseChainID(ctx.ChainID())
	if revision != height.GetRevisionNumber() {
		return nil, errorsmod.Wrapf(clienttypes.ErrInvalidHeight, "chainID revision number does not match height revision number: expected %d, got %d", revision, height.GetRevisionNumber())
	}
	histInfo, err := k.stakingKeeper.GetHistoricalInfo(ctx, int64(height.RevisionHeight))
	if err != nil {
		return nil, errorsmod.Wrapf(err, "height %d", height.RevisionHeight)
	}

	consensusState := &ibctmtypes.ConsensusState{
		Timestamp:          histInfo.Header.Time,
		Root:               commitmenttypes.NewMerkleRoot(histInfo.Header.GetAppHash()),
		NextValidatorsHash: histInfo.Header.NextValidatorsHash,
	}
	return consensusState, nil
}

// StopAndPrepareForConsumerRemoval sets the phase of the chain to stopped and prepares to get the state of the
// chain removed after unbonding period elapses
func (k Keeper) StopAndPrepareForConsumerRemoval(ctx sdk.Context, consumerId uint64) error {
	// The phase of the chain is immediately set to stopped, albeit its state is removed later (see below).
	// Setting the phase here helps in not considering this chain when we look at launched chains (e.g., in `QueueVSCPackets)
	k.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_STOPPED)

	// A stopped chain generates no further evidence or epoch fee distributions
	// to weigh pending downtime state against, so cancel it outright rather
	// than leave it to expire on its own (phase-1 review finding 3).
	if err := k.CancelConsumerDowntimeState(ctx, consumerId); err != nil {
		return fmt.Errorf("cancelling downtime state for consumer %d: %w", consumerId, err)
	}

	// Clear any pending pause auto-stop schedule: this chain is stopping now,
	// via this call, so there is nothing left to auto-stop later. A no-op
	// when the chain was never paused, which is the common case (e.g. the
	// liveness sweep stopping a launched consumer). CancelConsumerPauseExpiration
	// removes both the per-consumer entry and its id from the
	// pause-expiration time bucket, so no stale bucket entry survives removal.
	if err := k.CancelConsumerPauseExpiration(ctx, consumerId); err != nil {
		return fmt.Errorf("cancelling pause-expiration schedule for consumer %d: %w", consumerId, err)
	}

	// state of this chain is removed once UnbondingPeriod elapses
	unbondingPeriod, err := k.stakingKeeper.UnbondingTime(ctx)
	if err != nil {
		return err
	}
	removalTime := ctx.BlockTime().Add(unbondingPeriod)

	if err := k.SetConsumerRemovalTime(ctx, consumerId, removalTime); err != nil {
		return fmt.Errorf("cannot set removal time (%s): %s", removalTime.String(), err.Error())
	}
	if err := k.AppendConsumerToBeRemoved(ctx, consumerId, removalTime); err != nil {
		return errorsmod.Wrapf(vaastypes.ErrInvalidConsumerState, "cannot set consumer to be removed: %s", err.Error())
	}

	return nil
}

// PauseConsumerChain transitions a launched consumer chain into
// CONSUMER_PHASE_PAUSED following a successful downtime challenge (see
// MsgChallengeConsumerDowntime, wired into this path in a later task): the
// challenge proved the validator was live, so its pending downtime slash and
// this epoch's downtime marks for the consumer are cancelled via
// CancelConsumerDowntimeState. A paused consumer is excluded from VSC packet
// queuing (QueueVSCPackets iterates GetAllLaunchedConsumerIds), fee
// distribution, and evidence handling -- all of which require phase LAUNCHED.
//
// The pause is not indefinite: an auto-stop is scheduled at
// now + MaxPauseDuration via the pause-expiration queue consumed by
// BeginBlockAutoStopPausedConsumers, which calls StopAndPrepareForConsumerRemoval
// -- not DeleteConsumerChain -- since DeleteConsumerChain requires phase
// STOPPED and would reject a still-PAUSED consumer. This guarantees an
// unresolved pause deterministically becomes STOPPED-then-DELETED rather than
// stranding the consumer in PAUSED forever.
func (k Keeper) PauseConsumerChain(ctx sdk.Context, consumerId uint64) error {
	phase := k.GetConsumerPhase(ctx, consumerId)
	if phase != types.CONSUMER_PHASE_LAUNCHED {
		return errorsmod.Wrapf(vaastypes.ErrInvalidConsumerState,
			"cannot pause consumer %d: expected phase launched, got %s", consumerId, phase)
	}

	k.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_PAUSED)

	if err := k.CancelConsumerDowntimeState(ctx, consumerId); err != nil {
		return fmt.Errorf("cancelling downtime state for consumer %d: %w", consumerId, err)
	}

	autoStopTime := ctx.BlockTime().Add(k.GetMaxPauseDuration(ctx))
	if err := k.SetConsumerPauseExpirationTime(ctx, consumerId, autoStopTime); err != nil {
		return fmt.Errorf("setting pause expiration time for consumer %d: %w", consumerId, err)
	}
	if err := k.AppendConsumerToBeAutoStopped(ctx, consumerId, autoStopTime); err != nil {
		return errorsmod.Wrapf(vaastypes.ErrInvalidConsumerState,
			"cannot schedule auto-stop for consumer %d: %s", consumerId, err.Error())
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		vaastypes.EventTypeConsumerPaused,
		sdk.NewAttribute(types.AttributeConsumerId, fmt.Sprintf("%d", consumerId)),
	))

	return nil
}

// ResumeConsumerChain resumes a PAUSED consumer chain via MsgResumeConsumer.
//
// It requires the provider's IBC client of the consumer to have status
// Active: with defaults, MaxPauseDuration (30 days) exceeds the
// consumer-client trusting period, so a long pause with idle relayers can
// expire the client before a resume is proposed (see spec section 9,
// "Client expiry during a pause"). Resuming onto an expired or frozen client
// would strand the consumer LAUNCHED with no way to receive the resync
// below, so this fails instead and directs the caller to bundle ibc-go's
// MsgRecoverClient (client substitution, already governance-gated) into the
// same governance proposal as the resume.
//
// On success: cancels the scheduled auto-stop (CancelConsumerPauseExpiration),
// restores phase LAUNCHED, reseeds the liveness clock (so the liveness sweep
// does not immediately re-flag the consumer for the pause duration it was
// rightfully silent), and forces an immediate snapshot VSC packet -- queued
// and sent right away rather than left for the next epoch boundary, which
// could be up to BlocksPerEpoch blocks away -- so the consumer's cross-chain
// validator set is not stale for however long the pause lasted.
//
// The send is enforced, not best-effort: unlike the EndBlock path (which
// swallows send errors so a relayer hiccup never fails a block), this calls
// sendVSCPacketsToChainStrict and returns its error if the snapshot cannot
// actually be handed to IBC. Reporting success while the snapshot silently
// stayed queued would let the next epoch enqueue a diff on top of a
// consumer that never received the snapshot it was diffed against; under
// out-of-order IBC v2 delivery that diff can permanently corrupt the
// consumer's validator set. Failing here instead means the whole message
// execution rolls back (standard cosmos-sdk cache-context semantics): phase
// stays PAUSED and the auto-stop schedule is untouched, so governance can
// simply retry the resume once the underlying client/relayer issue is
// fixed, with no partial state left behind.
func (k Keeper) ResumeConsumerChain(ctx sdk.Context, consumerId uint64) error {
	phase := k.GetConsumerPhase(ctx, consumerId)
	if phase != types.CONSUMER_PHASE_PAUSED {
		return errorsmod.Wrapf(types.ErrInvalidPhase,
			"cannot resume consumer %d: expected phase paused, got %s", consumerId, phase)
	}

	clientId, found := k.GetConsumerClientId(ctx, consumerId)
	if !found {
		return errorsmod.Wrapf(types.ErrInvalidConsumerClient, "no client discovered for consumer %d", consumerId)
	}
	if status := k.clientKeeper.GetClientStatus(ctx, clientId); status != ibcexported.Active {
		return errorsmod.Wrapf(types.ErrConsumerClientNotActive,
			"consumer %d client %s has status %q; if expired or frozen, bundle ibc-go's MsgRecoverClient for this client in the same governance proposal as this resume",
			consumerId, clientId, status)
	}

	if err := k.CancelConsumerPauseExpiration(ctx, consumerId); err != nil {
		return fmt.Errorf("cancelling pause-expiration schedule for consumer %d: %w", consumerId, err)
	}

	k.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_LAUNCHED)

	if err := k.SetConsumerLastAckTime(ctx, consumerId, ctx.BlockTime()); err != nil {
		return fmt.Errorf("reseeding liveness clock for consumer %d: %w", consumerId, err)
	}

	if err := k.QueueImmediateSnapshotVSCPacket(ctx, consumerId); err != nil {
		return fmt.Errorf("queueing resume snapshot for consumer %d: %w", consumerId, err)
	}
	if err := k.sendVSCPacketsToChainStrict(ctx, consumerId, clientId); err != nil {
		return fmt.Errorf("sending resume snapshot for consumer %d: %w", consumerId, err)
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		vaastypes.EventTypeConsumerResumed,
		sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
		sdk.NewAttribute(types.AttributeConsumerId, fmt.Sprintf("%d", consumerId)),
	))

	return nil
}

// CancelConsumerDowntimeState clears all in-flight downtime-detection state
// for a consumer: downtime slashes accepted but not yet executed
// (PendingDowntimeSlashes), and the current epoch's downtime marks used to
// exclude validators from fee distribution (EpochDowntime). It is the shared
// primitive behind PauseConsumerChain, StopAndPrepareForConsumerRemoval, and
// DeleteConsumerChain (phase-1 review finding 3): once a consumer pauses,
// stops, or is deleted, there is no further epoch or challenge window to
// resolve this state against, so it is cancelled outright instead of left to
// expire on its own.
//
// LastPunishedWindowEnds and WithheldFeeRecords are deliberately left alone
// here -- they are only erased as part of full consumer removal, in
// DeleteConsumerChain.
func (k Keeper) CancelConsumerDowntimeState(ctx sdk.Context, consumerId uint64) error {
	if err := k.PendingDowntimeSlashes.Clear(ctx, collections.NewPrefixedPairRange[uint64, []byte](consumerId)); err != nil {
		return fmt.Errorf("clearing pending downtime slashes for consumer %d: %w", consumerId, err)
	}
	if err := k.EpochDowntime.Clear(ctx, collections.NewPrefixedPairRange[uint64, []byte](consumerId)); err != nil {
		return fmt.Errorf("clearing epoch downtime marks for consumer %d: %w", consumerId, err)
	}
	return nil
}

// BeginBlockAutoStopPausedConsumers stops any consumer whose pause
// (CONSUMER_PHASE_PAUSED) has outlived MaxPauseDuration, via the same
// StopAndPrepareForConsumerRemoval path SweepUnresponsiveConsumers uses. This
// guarantees a paused consumer never remains paused indefinitely: an
// unresolved pause always resolves to STOPPED (and, after the unbonding
// period, DELETED).
func (k Keeper) BeginBlockAutoStopPausedConsumers(ctx sdk.Context) error {
	consumerIds, err := k.ConsumeIdsFromTimeQueue(
		ctx,
		k.PauseExpirationTimeToConsumerIds,
		k.GetConsumersToBeAutoStopped,
		k.DeleteAllConsumersToBeAutoStopped,
		k.AppendConsumerToBeAutoStopped,
		200,
	)
	if err != nil {
		return errorsmod.Wrapf(vaastypes.ErrInvalidConsumerState, "getting consumers whose pause has expired: %s", err.Error())
	}
	for _, consumerId := range consumerIds {
		// Only still-paused consumers are stopped here: nothing in this task
		// resumes a paused consumer, but this guard keeps the sweep correct if
		// a future path (e.g. a resume message) does.
		if k.GetConsumerPhase(ctx, consumerId) != types.CONSUMER_PHASE_PAUSED {
			continue
		}

		cachedCtx, writeFn := ctx.CacheContext()
		if err := k.StopAndPrepareForConsumerRemoval(cachedCtx, consumerId); err != nil {
			k.Logger(ctx).Error("failed to auto-stop paused consumer whose pause expired",
				"consumerId", consumerId, "error", err.Error())
			continue
		}
		writeFn()
	}
	return nil
}

// BeginBlockRemoveConsumers removes stopped consumer chain for which the removal time has passed
func (k Keeper) BeginBlockRemoveConsumers(ctx sdk.Context) error {
	consumerIds, err := k.ConsumeIdsFromTimeQueue(
		ctx,
		k.RemovalTimeToConsumerIds,
		k.GetConsumersToBeRemoved,
		k.DeleteAllConsumersToBeRemoved,
		k.AppendConsumerToBeRemoved,
		200,
	)
	if err != nil {
		return errorsmod.Wrapf(vaastypes.ErrInvalidConsumerState, "getting consumers ready to stop: %s", err.Error())
	}
	for _, consumerId := range consumerIds {
		// delete consumer chain in a cached context to abort deletion in case of errors
		cachedCtx, writeFn := ctx.CacheContext()
		err = k.DeleteConsumerChain(cachedCtx, consumerId)
		if err != nil {
			k.Logger(ctx).Error("consumer chain could not be removed",
				"consumerId", consumerId,
				"error", err.Error())
			continue
		}

		writeFn()
	}
	return nil
}

// DeleteConsumerChain cleans up the state of the given consumer chain.
func (k Keeper) DeleteConsumerChain(ctx sdk.Context, consumerId uint64) (err error) {
	phase := k.GetConsumerPhase(ctx, consumerId)
	if phase != types.CONSUMER_PHASE_STOPPED {
		return fmt.Errorf("cannot delete non-stopped chain: %d", consumerId)
	}

	// Auto-sweep the fee pool. This cannot fail under valid state; on state
	// corruption it panics rather than returning, so deletion is never silently
	// aborted (which would strand the consumer in STOPPED with no way out).
	k.SweepConsumerFeePool(ctx, consumerId, nil)

	// clean up states
	k.DeleteConsumerClientId(ctx, consumerId)
	k.DeleteConsumerGenesis(ctx, consumerId)
	// Note: this call panics if the key assignment state is invalid
	k.DeleteKeyAssignments(ctx, consumerId)
	k.DeleteEquivocationEvidenceMinHeight(ctx, consumerId)

	k.DeleteInitChainHeight(ctx, consumerId)
	k.DeletePendingVSCPackets(ctx, consumerId)

	k.DeleteConsumerValSet(ctx, consumerId)

	k.DeleteConsumerRemovalTime(ctx, consumerId)
	k.DeleteConsumerLastAckTime(ctx, consumerId)
	k.DeleteConsumerHighestSentVscId(ctx, consumerId)
	k.DeleteConsumerHighestAckedVscId(ctx, consumerId)
	k.DeleteConsumerDebt(ctx, consumerId)

	if err := k.ConsumerFeesPerBlockOverride.Remove(ctx, consumerId); err != nil {
		if !errors.Is(err, collections.ErrNotFound) {
			return err
		}
	}
	// Removing the reverse-lookup entry can only fail on a store/codec error,
	// i.e. corruption; panic rather than abort the delete (see auto-sweep above).
	if err := k.FeePoolAddressToConsumerId.Remove(ctx, k.GetConsumerFeePoolAddress(consumerId)); err != nil {
		panic(fmt.Sprintf("delete consumer %d: remove fee-pool reverse lookup: %s", consumerId, err))
	}

	// Cancel any downtime state left over for this consumer. In the normal
	// path this is already a no-op: both StopAndPrepareForConsumerRemoval and
	// PauseConsumerChain (via BeginBlockAutoStopPausedConsumers) cancel it
	// before this chain ever reaches STOPPED. Calling it again here is cheap
	// and keeps deletion correct even if a future path reaches STOPPED some
	// other way.
	if err := k.CancelConsumerDowntimeState(ctx, consumerId); err != nil {
		return fmt.Errorf("cancelling downtime state for consumer %d: %w", consumerId, err)
	}

	// Unlike CancelConsumerDowntimeState's callers, deletion is the consumer's
	// full erasure: withheld fee escrow and infraction-window bookkeeping are
	// no longer meaningful once the consumer is gone, so they are wiped too
	// (phase-1 review finding 3).
	if err := k.WithheldFeeRecords.Clear(ctx, collections.NewPrefixedPairRange[uint64, []byte](consumerId)); err != nil {
		return fmt.Errorf("clearing withheld fee records for consumer %d: %w", consumerId, err)
	}
	if err := k.LastPunishedWindowEnds.Clear(ctx, collections.NewPrefixedPairRange[uint64, []byte](consumerId)); err != nil {
		return fmt.Errorf("clearing last punished window ends for consumer %d: %w", consumerId, err)
	}

	// Note that we do not delete ConsumerIdToChainIdKey and ConsumerIdToPhase, as well
	// as consumer metadata and initialization parameters.
	// This is to enable block explorers and front ends to show information of
	// consumer chains that were removed without needing an archive node.

	k.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_DELETED)
	k.Logger(ctx).Info("consumer chain deleted from provider", "consumerId", consumerId)

	return nil
}

// SweepUnresponsiveConsumers stops any launched consumer that has produced no
// successful VSC ack within the liveness grace period. It is the sole authority
// for removing unresponsive consumers and runs on the provider block clock, so
// it does not depend on a live IBC client or an available relayer.
func (k Keeper) SweepUnresponsiveConsumers(ctx sdk.Context) error {
	grace, err := k.LivenessGracePeriod(ctx)
	if err != nil {
		return err
	}
	for _, consumerId := range k.GetAllLaunchedConsumerIds(ctx) {
		lastAck := k.GetConsumerLastAckTime(ctx, consumerId)
		if ctx.BlockTime().Sub(lastAck) <= grace {
			continue
		}
		k.Logger(ctx).Info("consumer unresponsive past liveness grace, stopping",
			"consumerId", consumerId, "lastAck", lastAck, "grace", grace)
		// Stop in a cached context so a partial failure does not commit the
		// STOPPED phase without also scheduling removal (which would strand the
		// consumer STOPPED-but-never-DELETED). On error nothing is written and
		// the next sweep retries, mirroring BeginBlockRemoveConsumers.
		cachedCtx, writeFn := ctx.CacheContext()
		if err := k.StopAndPrepareForConsumerRemoval(cachedCtx, consumerId); err != nil {
			k.Logger(ctx).Error("failed to stop unresponsive consumer", "consumerId", consumerId, "error", err.Error())
			continue
		}
		writeFn()
	}
	return nil
}
