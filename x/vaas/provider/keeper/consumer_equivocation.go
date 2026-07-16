package keeper

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	tmtypes "github.com/cometbft/cometbft/types"

	ibcclienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

//
// Double Voting section
//

// HandleConsumerDoubleVoting verifies a double voting evidence for a given a consumer id
// and a public key and, if successful, executes the slashing, jailing, and tombstoning of the malicious validator.
func (k Keeper) HandleConsumerDoubleVoting(
	ctx sdk.Context,
	consumerId uint64,
	evidence *tmtypes.DuplicateVoteEvidence,
	pubkey cryptotypes.PubKey,
) error {
	// check that the evidence is for an ICS consumer chain
	if k.GetConsumerPhase(ctx, consumerId) != types.CONSUMER_PHASE_LAUNCHED {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidDoubleVotingEvidence,
			"consumer chain %d is not launched",
			consumerId,
		)
	}

	// check that the evidence is not too old
	minHeight := k.GetEquivocationEvidenceMinHeight(ctx, consumerId)
	if uint64(evidence.VoteA.Height) < minHeight {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidDoubleVotingEvidence,
			"evidence for consumer chain %d is too old - evidence height (%d), min (%d)",
			consumerId,
			evidence.VoteA.Height,
			minHeight,
		)
	}

	// get the chainId of this consumer chain to verify the double-voting evidence
	chainId, err := k.GetConsumerChainId(ctx, consumerId)
	if err != nil {
		return err
	}

	// verifies the double voting evidence using the consumer chain public key
	if err = k.VerifyDoubleVotingEvidence(*evidence, chainId, pubkey); err != nil {
		return err
	}

	// get the validator's consensus address on the provider
	providerAddr := k.GetProviderAddrFromConsumerAddr(
		ctx,
		consumerId,
		types.NewConsumerConsAddress(sdk.ConsAddress(evidence.VoteA.ValidatorAddress.Bytes())),
	)

	// get infraction parameters
	infractionParams := k.GetInfractionParams(ctx)

	alreadyTombstoned := false
	if err = k.SlashValidator(ctx, providerAddr, infractionParams.DoubleSign, stakingtypes.Infraction_INFRACTION_DOUBLE_SIGN); err != nil {
		// Make repeated (already-processed) evidence submissions idempotent.
		if errors.Is(err, slashingtypes.ErrValidatorTombstoned) {
			alreadyTombstoned = true
		} else {
			return err
		}
	}

	if !alreadyTombstoned {
		if err = k.JailAndTombstoneValidator(ctx, providerAddr, infractionParams.DoubleSign); err != nil {
			if errors.Is(err, slashingtypes.ErrValidatorTombstoned) {
				alreadyTombstoned = true
			} else {
				return err
			}
		}
	}

	k.Logger(ctx).Info(
		"confirmed equivocation",
		"consumerId", consumerId,
		"chainId", chainId,
		"byzantine validator address", providerAddr.String(),
		"already_tombstoned", alreadyTombstoned,
	)

	return nil
}

// VerifyDoubleVotingEvidence verifies a double voting evidence
// for a given chain id and a validator public key
func (k Keeper) VerifyDoubleVotingEvidence(
	evidence tmtypes.DuplicateVoteEvidence,
	chainId string,
	pubkey cryptotypes.PubKey,
) error {
	if pubkey == nil {
		return fmt.Errorf("validator public key cannot be empty")
	}

	// check that the validator address in the evidence is derived from the provided public key
	if !bytes.Equal(pubkey.Address(), evidence.VoteA.ValidatorAddress) {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidDoubleVotingEvidence,
			"public key %s doesn't correspond to the validator address %s in double vote evidence",
			pubkey.String(), evidence.VoteA.ValidatorAddress.String(),
		)
	}

	// Note the age of the evidence isn't checked.

	// height/round/type must be the same
	if evidence.VoteA.Height != evidence.VoteB.Height ||
		evidence.VoteA.Round != evidence.VoteB.Round ||
		evidence.VoteA.Type != evidence.VoteB.Type {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidDoubleVotingEvidence,
			"height/round/type are not the same: %d/%d/%v vs %d/%d/%v",
			evidence.VoteA.Height, evidence.VoteA.Round, evidence.VoteA.Type,
			evidence.VoteB.Height, evidence.VoteB.Round, evidence.VoteB.Type)
	}

	// Addresses must be the same
	if !bytes.Equal(evidence.VoteA.ValidatorAddress, evidence.VoteB.ValidatorAddress) {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidDoubleVotingEvidence,
			"validator addresses do not match: %X vs %X",
			evidence.VoteA.ValidatorAddress,
			evidence.VoteB.ValidatorAddress,
		)
	}

	// BlockIDs must be different
	if evidence.VoteA.BlockID.Equals(evidence.VoteB.BlockID) {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidDoubleVotingEvidence,
			"block IDs are the same (%v) - not a real duplicate vote",
			evidence.VoteA.BlockID,
		)
	}

	va := evidence.VoteA.ToProto()
	vb := evidence.VoteB.ToProto()

	// signatures must be valid
	if !pubkey.VerifySignature(tmtypes.VoteSignBytes(chainId, va), evidence.VoteA.Signature) {
		return fmt.Errorf("verifying VoteA: %w", tmtypes.ErrVoteInvalidSignature)
	}
	if !pubkey.VerifySignature(tmtypes.VoteSignBytes(chainId, vb), evidence.VoteB.Signature) {
		return fmt.Errorf("verifying VoteB: %w", tmtypes.ErrVoteInvalidSignature)
	}

	return nil
}

//
// Consumer-initiated slashing section
//

// HandleConsumerEvidencePacket handles an evidence packet received from a consumer chain.
// It dispatches to the appropriate handler based on the infraction type.
func (k Keeper) HandleConsumerEvidencePacket(ctx sdk.Context, consumerId uint64, evidencePacket vaastypes.EvidencePacketData) error {
	if err := evidencePacket.Validate(); err != nil {
		return errorsmod.Wrapf(vaastypes.ErrInvalidPacketData, "invalid evidence packet: %s", err)
	}

	if k.GetConsumerPhase(ctx, consumerId) != types.CONSUMER_PHASE_LAUNCHED {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidConsumerState,
			"consumer chain %d is not launched (phase: %s)",
			consumerId,
			k.GetConsumerPhase(ctx, consumerId),
		)
	}

	switch evidencePacket.Infraction {
	case stakingtypes.Infraction_INFRACTION_DOWNTIME:
		return k.HandleConsumerDowntime(ctx, consumerId, evidencePacket)
	default:
		return fmt.Errorf("unsupported infraction type in evidence packet: %s", evidencePacket.Infraction)
	}
}

// HandleConsumerDowntime validates a consumer's downtime evidence and, if
// accepted, queues a slash behind the downtime challenge window instead of
// executing it immediately. The provider verifies:
//  1. The echoed downtime params (window, min-signed fraction) are acceptable
//     -- they match the provider's current params, or a recently-superseded
//     set (see AcceptableDowntimeParams).
//  2. The reported missed-block count exceeds the infraction threshold
//     computed from those echoed params (EvidencePacketData.MaxMissed).
//  3. The infraction height is not older than the minimum evidence height for
//     this consumer, and the validator was part of the consumer's validator
//     set.
//  4. The window-end time -- anchored to an IBC consensus state proving the
//     consumer chain actually reached that height, see windowEndTimestamp --
//     is past the consumer's downtime grace period and not older than
//     DowntimeEvidenceMaxAge.
//  5. The window starts above the pair's pruned acceptance floor
//     (DowntimeWindowFloors) and does not intersect any window already
//     accepted for this (consumer, validator) pair
//     (AcceptedDowntimeWindows). Multiple disjoint windows may be pending
//     for the same pair at once, and acceptance is order-independent: the
//     delivery order of disjoint windows never affects which are accepted.
//
// On acceptance the slash amount is priced now, at receipt time (see
// ResolveDowntimeSlashTokens), and stored as a PendingDowntimeSlash maturing
// DowntimeChallengeWindow from now, keyed by (consumer, validator, window-end
// height) so it coexists with any other window still pending for the same
// pair. Execution happens later, once matured, in the epoch sweep: this
// handler never calls SlashValidator.
//
// CONTRACT: A downtime infraction must never jail a validator.
func (k Keeper) HandleConsumerDowntime(ctx sdk.Context, consumerId uint64, evidencePacket vaastypes.EvidencePacketData) error {
	echoedParams := vaastypes.DowntimeParams{
		SignedBlocksWindow: evidencePacket.SignedBlocksWindow,
		MinSignedPerWindow: evidencePacket.MinSignedPerWindow,
	}
	if !k.AcceptableDowntimeParams(ctx, echoedParams) {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidPacketData,
			"downtime evidence for consumer chain %d echoes unacceptable downtime params (window %d, min signed %s)",
			consumerId,
			evidencePacket.SignedBlocksWindow,
			evidencePacket.MinSignedPerWindow,
		)
	}

	maxMissed := evidencePacket.MaxMissed()
	if evidencePacket.MissedCount() <= maxMissed {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidPacketData,
			"downtime evidence for consumer chain %d does not exceed the infraction threshold: missed %d, max %d",
			consumerId,
			evidencePacket.MissedCount(),
			maxMissed,
		)
	}

	consumerAddr := types.NewConsumerConsAddress(evidencePacket.ValidatorAddr)
	providerAddr := k.GetProviderAddrFromConsumerAddr(ctx, consumerId, consumerAddr)

	// Verify the infraction height is not too old.
	minHeight := k.GetEquivocationEvidenceMinHeight(ctx, consumerId)
	if uint64(evidencePacket.InfractionHeight) < minHeight {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidPacketData,
			"downtime evidence for consumer chain %d is too old: infraction height (%d), min (%d)",
			consumerId,
			evidencePacket.InfractionHeight,
			minHeight,
		)
	}

	// Verify the validator was part of the consumer's validator set. Join-time
	// trimming of the bitmap happens consumer-side, so there is no join-height
	// comparison here.
	if _, found := k.GetConsumerValidator(ctx, consumerId, providerAddr); !found {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidPacketData,
			"validator %s is not in the validator set of consumer chain %d",
			providerAddr.String(),
			consumerId,
		)
	}

	clientId, found := k.GetConsumerClientId(ctx, consumerId)
	if !found {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidConsumerState,
			"no IBC client found for consumer chain %d",
			consumerId,
		)
	}

	// Anchor the window-end time to a real IBC consensus state so the grace
	// and evidence-age checks below are judged against consumer chain time
	// the provider can actually verify, not the packet's own unverified claim.
	windowEndTime, err := k.windowEndTimestamp(ctx, clientId, evidencePacket.InfractionHeight)
	if err != nil {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidPacketData,
			"cannot anchor downtime evidence window for consumer chain %d: %s",
			consumerId, err,
		)
	}

	// Check that the consumer chain is outside its downtime grace period.
	// During the grace period after launch, downtime evidence is suppressed to give
	// validators time to spin up their consumer chain nodes.
	infractionParams := k.GetInfractionParams(ctx)
	if infractionParams.DowntimeGracePeriod > 0 {
		initParams, err := k.GetConsumerInitializationParameters(ctx, consumerId)
		if err != nil {
			return errorsmod.Wrapf(
				vaastypes.ErrInvalidConsumerState,
				"cannot get initialization parameters for consumer chain %d: %s",
				consumerId, err,
			)
		}
		gracePeriodEnd := initParams.SpawnTime.Add(infractionParams.DowntimeGracePeriod)
		if windowEndTime.Before(gracePeriodEnd) {
			return errorsmod.Wrapf(
				vaastypes.ErrInvalidPacketData,
				"consumer chain %d is still in downtime grace period (launched %s, grace ends %s, window end %s)",
				consumerId,
				initParams.SpawnTime,
				gracePeriodEnd,
				windowEndTime,
			)
		}
	}

	// Verify the evidence isn't stale relative to when it was submitted.
	if age := ctx.BlockTime().Sub(windowEndTime); age > infractionParams.DowntimeEvidenceMaxAge {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidPacketData,
			"downtime evidence for consumer chain %d is too old: window ended %s ago, max age %s",
			consumerId,
			age,
			infractionParams.DowntimeEvidenceMaxAge,
		)
	}

	pairKey := collections.Join(consumerId, providerAddr.ToSdkConsAddr().Bytes())

	// Reject evidence at or below the pair's pruned acceptance floor: the
	// AcceptedDowntimeWindows records covering those heights have been
	// pruned, so the intersection check below cannot vouch for them
	// (see pruneAcceptedDowntimeWindows for why nothing acceptable can sit
	// down there).
	floor, err := k.DowntimeWindowFloors.Get(ctx, pairKey)
	if err == nil {
		if evidencePacket.WindowStartHeight <= floor {
			return errorsmod.Wrapf(
				vaastypes.ErrInvalidPacketData,
				"downtime evidence for consumer chain %d is at or below the pruned acceptance floor for validator %s: window start height %d, floor %d",
				consumerId,
				providerAddr.String(),
				evidencePacket.WindowStartHeight,
				floor,
			)
		}
	} else if !errors.Is(err, collections.ErrNotFound) {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidPacketData,
			"checking downtime window floor for consumer chain %d: %s",
			consumerId, err,
		)
	}

	// Reject evidence whose window intersects one already accepted for this
	// (consumer, validator) pair, even after the earlier pending slash has
	// matured, executed, and been removed from PendingDowntimeSlashes --
	// otherwise the same infraction could be re-submitted and slashed a
	// second time once its original pending entry is gone. Checking
	// intersection against every retained record, rather than against a
	// single boundary, keeps acceptance order-independent: IBC v2 delivery
	// is out of order and relaying is permissionless, so accepting a later
	// window first must not void earlier windows still in flight. Accepted
	// windows for a pair are pairwise disjoint as a consequence.
	if err := k.checkAcceptedDowntimeWindowIntersection(
		ctx, consumerId, providerAddr, evidencePacket.WindowStartHeight, evidencePacket.InfractionHeight,
	); err != nil {
		return err
	}

	slashTokens, err := k.ResolveDowntimeSlashTokens(ctx, consumerId, evidencePacket, windowEndTime)
	if err != nil {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidPacketData,
			"pricing downtime slash for consumer chain %d: %s",
			consumerId, err,
		)
	}

	// Record downtime for epoch reward exclusion. This takes effect
	// immediately, independent of the slash challenge window below.
	k.MarkEpochDowntime(ctx, consumerId, providerAddr.ToSdkConsAddr())

	maturesAt := ctx.BlockTime().Add(infractionParams.DowntimeChallengeWindow)
	pending := types.PendingDowntimeSlash{
		ConsumerId:         consumerId,
		ProviderConsAddr:   providerAddr.ToSdkConsAddr().Bytes(),
		WindowStartHeight:  evidencePacket.WindowStartHeight,
		Span:               evidencePacket.Span(),
		MissedCount:        evidencePacket.MissedCount(),
		MissedBlocksBitmap: evidencePacket.MissedBlocksBitmap,
		SlashTokens:        slashTokens,
		MaturesAt:          maturesAt,
	}
	// Keyed by (consumer, validator, window-end height) so this window's
	// entry coexists with any other window still pending for the same pair.
	pendingKey := collections.Join3(consumerId, providerAddr.ToSdkConsAddr().Bytes(), evidencePacket.InfractionHeight)
	if err := k.PendingDowntimeSlashes.Set(ctx, pendingKey, pending); err != nil {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidPacketData,
			"storing pending downtime slash for consumer chain %d: %s",
			consumerId, err,
		)
	}
	if err := k.AcceptedDowntimeWindows.Set(ctx, pendingKey, types.AcceptedDowntimeWindow{
		WindowStart: evidencePacket.WindowStartHeight,
		AcceptedAt:  ctx.BlockTime(),
	}); err != nil {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidPacketData,
			"storing accepted downtime window for consumer chain %d: %s",
			consumerId, err,
		)
	}

	k.Logger(ctx).Info(
		"queued consumer downtime slash",
		"consumerId", consumerId,
		"consumerAddr", consumerAddr.String(),
		"providerAddr", providerAddr.String(),
		"windowStartHeight", evidencePacket.WindowStartHeight,
		"windowEndHeight", evidencePacket.InfractionHeight,
		"missedCount", evidencePacket.MissedCount(),
		"slashTokens", slashTokens.String(),
		"maturesAt", maturesAt,
	)

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			vaastypes.EventTypePendingDowntimeSlash,
			sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
			sdk.NewAttribute(types.AttributeConsumerId, fmt.Sprintf("%d", consumerId)),
			sdk.NewAttribute(vaastypes.AttributeProviderValidatorAddress, providerAddr.String()),
			sdk.NewAttribute(vaastypes.AttributeWindowStartHeight, fmt.Sprintf("%d", evidencePacket.WindowStartHeight)),
			sdk.NewAttribute(vaastypes.AttributeInfractionHeight, fmt.Sprintf("%d", evidencePacket.InfractionHeight)),
			sdk.NewAttribute(vaastypes.AttributeMissedCount, fmt.Sprintf("%d", evidencePacket.MissedCount())),
			sdk.NewAttribute(vaastypes.AttributeMissedBlocksBitmap, hex.EncodeToString(evidencePacket.MissedBlocksBitmap)),
			sdk.NewAttribute(vaastypes.AttributeSlashTokens, slashTokens.String()),
			sdk.NewAttribute(vaastypes.AttributeMaturesAt, maturesAt.String()),
		),
	)

	return nil
}

// checkAcceptedDowntimeWindowIntersection rejects a downtime evidence window
// [newStart, newEnd] that intersects any AcceptedDowntimeWindows record
// retained for (consumerId, providerAddr). Two closed height ranges
// intersect iff newStart <= retainedEnd && retainedStart <= newEnd. The
// sub-range scanned is bounded by however many accepted windows are
// currently retained for this single validator on this single consumer.
func (k Keeper) checkAcceptedDowntimeWindowIntersection(
	ctx sdk.Context, consumerId uint64, providerAddr types.ProviderConsAddress, newStart, newEnd int64,
) error {
	iter, err := k.AcceptedDowntimeWindows.Iterate(
		ctx, collections.NewSuperPrefixedTripleRange[uint64, []byte, int64](consumerId, providerAddr.ToSdkConsAddr().Bytes()),
	)
	if err != nil {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidPacketData,
			"checking accepted downtime windows for consumer chain %d: %s",
			consumerId, err,
		)
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			return errorsmod.Wrapf(
				vaastypes.ErrInvalidPacketData,
				"reading accepted downtime window for consumer chain %d: %s",
				consumerId, err,
			)
		}
		retainedStart, retainedEnd := kv.Value.WindowStart, kv.Key.K3()
		if newStart <= retainedEnd && retainedStart <= newEnd {
			return errorsmod.Wrapf(
				vaastypes.ErrInvalidPacketData,
				"downtime evidence for consumer chain %d intersects a window already accepted for validator %s: window [%d, %d], accepted window [%d, %d]",
				consumerId,
				providerAddr.String(),
				newStart,
				newEnd,
				retainedStart,
				retainedEnd,
			)
		}
	}
	return nil
}

// windowEndTimestamp resolves the timestamp anchor for a downtime evidence
// window: the timestamp of the smallest IBC consensus state stored for
// clientId at a height >= windowEnd. This proves the consumer chain actually
// reached at least that height, so the grace-period and evidence-age checks
// in HandleConsumerDowntime are judged against verifiable consumer chain
// time rather than the packet's own unverified claims. Returns an error if
// no such consensus state is stored.
func (k Keeper) windowEndTimestamp(ctx sdk.Context, clientId string, windowEnd int64) (time.Time, error) {
	if k.windowEndTimestampFn != nil {
		return k.windowEndTimestampFn(ctx, clientId, windowEnd)
	}

	clientStore := k.clientKeeper.GetStoreProvider().ClientStore(ctx, clientId)

	var anchorHeight ibcexported.Height
	found := false
	ibctmtypes.IterateConsensusStateAscending(clientStore, func(height ibcexported.Height) bool {
		if int64(height.GetRevisionHeight()) >= windowEnd {
			anchorHeight = height
			found = true
			return true
		}
		return false
	})
	if !found {
		return time.Time{}, fmt.Errorf("cannot anchor evidence window time")
	}

	consensusState, ok := k.clientKeeper.GetClientConsensusState(ctx, clientId, anchorHeight)
	if !ok {
		return time.Time{}, fmt.Errorf("cannot anchor evidence window time")
	}
	return time.Unix(0, int64(consensusState.GetTimestamp())), nil //nolint:staticcheck
}

// WindowEndTimestampForTest exposes windowEndTimestamp for tests exercising
// the real IBC-client-backed anchor resolution -- i.e. with
// windowEndTimestampFn left unset, unlike OverrideWindowEndTimestampForTest.
// Production code never calls this directly; HandleConsumerDowntime always
// goes through windowEndTimestamp itself.
func (k Keeper) WindowEndTimestampForTest(ctx sdk.Context, clientId string, windowEnd int64) (time.Time, error) {
	return k.windowEndTimestamp(ctx, clientId, windowEnd)
}

//
// Light Client Attack (IBC misbehavior) section
//

// HandleConsumerMisbehaviour checks if the given IBC misbehaviour corresponds to an equivocation light client attack.
//
// VAAS deliberately treats light-client misbehaviour as detection-only: the
// evidence is verified and the byzantine set is logged, but no slashing,
// jailing, or tombstoning happens here. The README and DESIGN_RATIONALE
// document this as the intended posture ("Light Client Misbehavior:
// detection and logging"). Validator punishment for equivocation goes through
// MsgSubmitConsumerDoubleVoting / HandleConsumerDoubleVoting, which slashes
// against a cryptographically self-contained DuplicateVoteEvidence. Light-
// client misbehaviour evidence is more involved to verify end-to-end against
// a buggy or adversarial consumer chain, and double-vote evidence already
// covers the same validator-equivocation case; punishing twice along two
// different paths is unnecessary.
//
// Do not "fix" this back to slash/jail/tombstone without revisiting that
// design choice.
//
// Returns the byzantine validators identified from the conflicting headers
// (provider-side consensus addresses). The slice may be empty: for amnesia
// attacks the byzantine set is unidentifiable by construction (see
// GetByzantineValidators). Callers should still surface that result to the
// submitter rather than treating it as a silent no-op.
func (k Keeper) HandleConsumerMisbehaviour(ctx sdk.Context, consumerId uint64, misbehaviour ibctmtypes.Misbehaviour) ([]types.ProviderConsAddress, error) {
	logger := k.Logger(ctx)

	// Check that the misbehaviour is valid and that the client consensus states at trusted heights are within trusting period
	if err := k.CheckMisbehaviour(ctx, consumerId, misbehaviour); err != nil {
		logger.Info("misbehaviour rejected", "error", err.Error())

		return nil, err
	}

	// Since the misbehaviour packet was received within the trusting period
	// w.r.t to the trusted consensus states the infraction age
	// isn't too old. see ibc-go/modules/light-clients/07-tendermint/types/misbehaviour_handle.go

	// Get Byzantine validators from the conflicting headers
	byzantineValidators, err := k.GetByzantineValidators(ctx, misbehaviour)
	if err != nil {
		return nil, err
	}

	provAddrs := make([]types.ProviderConsAddress, 0, len(byzantineValidators))
	for _, v := range byzantineValidators {
		providerAddr := k.GetProviderAddrFromConsumerAddr(
			ctx,
			consumerId,
			types.NewConsumerConsAddress(sdk.ConsAddress(v.Address.Bytes())),
		)
		provAddrs = append(provAddrs, providerAddr)
	}

	if len(provAddrs) == 0 {
		// Either the conflict is an amnesia attack (different commit rounds,
		// no state-transition conflict — byzantine set is unknowable) or the
		// two header signatures had no overlapping signer (very unusual given
		// CheckMisbehaviour just verified both headers).
		logger.Info(
			"confirmed light client attack with unidentifiable byzantine validators (no slashing applied)",
			"consumerId", consumerId,
			"chainId", misbehaviour.Header1.Header.ChainID,
		)
	} else {
		logger.Info(
			"confirmed equivocation light client attack (no slashing applied)",
			"consumerId", consumerId,
			"chainId", misbehaviour.Header1.Header.ChainID,
			"byzantine_validators", provAddrs,
		)
	}

	return provAddrs, nil
}

// GetByzantineValidators returns the validators that signed both headers.
// If the misbehavior is an equivocation light client attack, then these
// validators are the Byzantine validators.
func (k Keeper) GetByzantineValidators(ctx sdk.Context, misbehaviour ibctmtypes.Misbehaviour) (validators []*tmtypes.Validator, err error) {
	// construct the trusted and conflicted light blocks
	lightBlock1, err := headerToLightBlock(*misbehaviour.Header1)
	if err != nil {
		return validators, err
	}
	lightBlock2, err := headerToLightBlock(*misbehaviour.Header2)
	if err != nil {
		return validators, err
	}

	// Check if the misbehaviour corresponds to an Amnesia attack,
	// meaning that the conflicting headers have both valid state transitions
	// and different commit rounds. In this case, we return no validators as
	// we can't identify the byzantine validators.
	//
	// Note that we cannot differentiate which of the headers is trusted or malicious,
	if !headersStateTransitionsAreConflicting(*lightBlock1.Header, *lightBlock2.Header) && lightBlock1.Commit.Round != lightBlock2.Commit.Round {
		return validators, nil
	}

	// compare the signatures of the headers
	// and return the intersection of validators who signed both

	// create a map with the validators' address that signed header1
	header1Signers := map[string]int{}
	for idx, sign := range lightBlock1.Commit.Signatures {
		if sign.BlockIDFlag == tmtypes.BlockIDFlagAbsent {
			continue
		}
		header1Signers[sign.ValidatorAddress.String()] = idx
	}

	// iterate over the header2 signers and check if they signed header1
	for sigIdxHeader2, sign := range lightBlock2.Commit.Signatures {
		if sign.BlockIDFlag == tmtypes.BlockIDFlagAbsent {
			continue
		}
		if sigIdxHeader1, ok := header1Signers[sign.ValidatorAddress.String()]; ok {
			if err := verifyLightBlockCommitSig(*lightBlock1, sigIdxHeader1); err != nil {
				return nil, err
			}

			if err := verifyLightBlockCommitSig(*lightBlock2, sigIdxHeader2); err != nil {
				return nil, err
			}

			_, val := lightBlock1.ValidatorSet.GetByAddress(sign.ValidatorAddress)
			validators = append(validators, val)
		}
	}

	return validators, nil
}

// headerToLightBlock returns a CometBFT light block from the given IBC header
func headerToLightBlock(h ibctmtypes.Header) (*tmtypes.LightBlock, error) {
	sh, err := tmtypes.SignedHeaderFromProto(h.SignedHeader)
	if err != nil {
		return nil, err
	}

	vs, err := tmtypes.ValidatorSetFromProto(h.ValidatorSet)
	if err != nil {
		return nil, err
	}

	return &tmtypes.LightBlock{
		SignedHeader: sh,
		ValidatorSet: vs,
	}, nil
}

// CheckMisbehaviour checks that headers in the given misbehaviour forms
// a valid light client attack from an ICS consumer chain and that the light client isn't expired
func (k Keeper) CheckMisbehaviour(ctx sdk.Context, consumerId uint64, misbehaviour ibctmtypes.Misbehaviour) error {
	chainId := misbehaviour.Header1.Header.ChainID

	consumerChainId, err := k.GetConsumerChainId(ctx, consumerId)
	if err != nil {
		return err
	} else if consumerChainId != chainId {
		return fmt.Errorf("incorrect misbehaviour for a different chain id (%s) than that of the consumer chain %s (consumerId: %d)",
			chainId,
			consumerChainId,
			consumerId)
	}

	// check that the misbehaviour is for an ICS consumer chain
	clientId, found := k.GetConsumerClientId(ctx, consumerId)
	if !found {
		return fmt.Errorf("incorrect misbehaviour with conflicting headers from a non-existent consumer chain (consumerId: %d)", consumerId)
	} else if misbehaviour.ClientId != clientId {
		return fmt.Errorf("incorrect misbehaviour: expected client ID for consumer chain with id %d is %s got %s",
			consumerId,
			clientId,
			misbehaviour.ClientId,
		)
	}

	// Check that the headers are at the same height to ensure that
	// the misbehaviour is for a light client attack and not a time violation,
	// see ibc-go/modules/light-clients/07-tendermint/types/misbehaviour_handle.go
	if !misbehaviour.Header1.GetHeight().EQ(misbehaviour.Header2.GetHeight()) {
		return errorsmod.Wrap(ibcclienttypes.ErrInvalidMisbehaviour, "headers are not at same height")
	}

	// Check that the evidence is not too old
	minHeight := k.GetEquivocationEvidenceMinHeight(ctx, consumerId)
	evidenceHeight := misbehaviour.Header1.GetHeight().GetRevisionHeight()
	// Note that the revision number is not relevant for checking the age of evidence
	// as it's already part of the chain ID and the minimum height is mapped to chain IDs
	if evidenceHeight < minHeight {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidDoubleVotingEvidence,
			"evidence for consumer chain %d is too old - evidence height (%d), min (%d)",
			consumerId,
			evidenceHeight,
			minHeight,
		)
	}

	lightClientModule := ibctmtypes.NewLightClientModule(k.cdc, k.clientKeeper.GetStoreProvider())

	// CheckForMisbehaviour verifies that the headers have different blockID hashes
	ok := lightClientModule.CheckForMisbehaviour(ctx, clientId, &misbehaviour)
	if !ok {
		return errorsmod.Wrapf(ibcclienttypes.ErrInvalidMisbehaviour, "invalid misbehaviour for client-id: %s", misbehaviour.ClientId)
	}

	// VerifyClientMessage calls verifyMisbehaviour which verifies that the headers in the misbehaviour
	// are valid against their respective trusted consensus states and that at least a TrustLevel of the validator set signed their commit,
	// see checkMisbehaviourHeader in ibc-go/blob/v7.3.0/modules/light-clients/07-tendermint/misbehaviour_handle.go#L126
	if err := lightClientModule.VerifyClientMessage(ctx, clientId, &misbehaviour); err != nil {
		return err
	}

	return nil
}

// Check if the given block headers have conflicting state transitions.
// Note that this method was copied from ConflictingHeaderIsInvalid in CometBFT,
// see https://github.com/cometbft/cometbft/blob/v0.34.27/types/evidence.go#L285
func headersStateTransitionsAreConflicting(h1, h2 tmtypes.Header) bool {
	return !bytes.Equal(h1.ValidatorsHash, h2.ValidatorsHash) ||
		!bytes.Equal(h1.NextValidatorsHash, h2.NextValidatorsHash) ||
		!bytes.Equal(h1.ConsensusHash, h2.ConsensusHash) ||
		!bytes.Equal(h1.AppHash, h2.AppHash) ||
		!bytes.Equal(h1.LastResultsHash, h2.LastResultsHash)
}

func verifyLightBlockCommitSig(lightBlock tmtypes.LightBlock, sigIdx int) error {
	// get signature
	sig := lightBlock.Commit.Signatures[sigIdx]

	// get validator
	idx, val := lightBlock.ValidatorSet.GetByAddress(sig.ValidatorAddress)
	if idx == -1 {
		return fmt.Errorf("incorrect signature: validator address %s isn't part of the validator set", sig.ValidatorAddress.String())
	}

	// verify validator pubkey corresponds to signature validator address
	if !bytes.Equal(val.PubKey.Address(), sig.ValidatorAddress) {
		return fmt.Errorf("validator public key doesn't correspond to signature validator address: %s!= %s", val.PubKey.Address(), sig.ValidatorAddress)
	}

	// validate signature
	voteSignBytes := lightBlock.Commit.VoteSignBytes(lightBlock.ChainID, int32(sigIdx))
	if !val.PubKey.VerifySignature(voteSignBytes, sig.Signature) {
		return fmt.Errorf("wrong signature (#%d): %X", sigIdx, sig.Signature)
	}

	return nil
}

//
// Punish Validator section
//

// JailAndTombstoneValidator jails and tombstones the validator with the given provider consensus address
func (k Keeper) JailAndTombstoneValidator(ctx sdk.Context, providerAddr types.ProviderConsAddress, jailingParams *types.SlashJailParameters) error {
	validator, err := k.stakingKeeper.GetValidatorByConsAddr(ctx, providerAddr.ToSdkConsAddr())
	if err != nil && errors.Is(err, stakingtypes.ErrNoValidatorFound) {
		return errorsmod.Wrapf(slashingtypes.ErrNoValidatorForAddress, "provider consensus address: %s", providerAddr.String())
	} else if err != nil {
		return errorsmod.Wrapf(slashingtypes.ErrBadValidatorAddr, "unknown error looking for provider consensus address: %s", providerAddr.String())
	}

	if validator.IsUnbonded() {
		return errorsmod.Wrapf(stakingtypes.ErrNoUnbondingDelegation, "validator is unbonded. provider consensus address: %s", providerAddr.String())
	}

	if k.slashingKeeper.IsTombstoned(ctx, providerAddr.ToSdkConsAddr()) {
		return errorsmod.Wrapf(slashingtypes.ErrValidatorTombstoned, "provider consensus address: %s", providerAddr.String())
	}

	// jail validator if not already
	if !validator.IsJailed() {
		err := k.stakingKeeper.Jail(ctx, providerAddr.ToSdkConsAddr())
		if err != nil {
			return err
		}
	}

	jailEndTime := ctx.BlockTime().Add(jailingParams.JailDuration)
	err = k.slashingKeeper.JailUntil(ctx, providerAddr.ToSdkConsAddr(), jailEndTime)
	if err != nil {
		return fmt.Errorf("fail to set jail duration for validator: %s: %s", providerAddr.String(), err)
	}

	if jailingParams.Tombstone {
		// Tombstone the validator so that we cannot slash the validator more than once
		// Note that we cannot simply use the fact that a validator is jailed to avoid slashing more than once
		// because then a validator could i) perform an equivocation, ii) get jailed (e.g., through downtime)
		// and in such a case the validator would not get slashed when we call `SlashValidator`.
		if err = k.slashingKeeper.Tombstone(ctx, providerAddr.ToSdkConsAddr()); err != nil {
			return fmt.Errorf("fail to tombstone validator: %s: %s", providerAddr.String(), err)
		}
	}

	return nil
}

// ComputePowerToSlash computes the power to be slashed based on the tokens in non-matured `undelegations` and
// `redelegations`, as well as the current `power` of the validator.
// Note that this method does not perform any slashing.
func (k Keeper) ComputePowerToSlash(ctx sdk.Context, validator stakingtypes.Validator, undelegations []stakingtypes.UnbondingDelegation,
	redelegations []stakingtypes.Redelegation, power int64, powerReduction math.Int,
) int64 {
	// compute the total numbers of tokens currently being undelegated
	undelegationsInTokens := math.NewInt(0)

	// Note that we use a **cached** context to avoid any actual slashing of undelegations or redelegations.
	cachedCtx, _ := ctx.CacheContext()
	for _, u := range undelegations {
		// v50: errors are ignored
		amountSlashed, _ := k.stakingKeeper.SlashUnbondingDelegation(cachedCtx, u, 0, math.LegacyNewDec(1))
		undelegationsInTokens = undelegationsInTokens.Add(amountSlashed)
	}

	// compute the total numbers of tokens currently being redelegated
	redelegationsInTokens := math.NewInt(0)
	for _, r := range redelegations {
		// v50 errors are ignored
		amountSlashed, _ := k.stakingKeeper.SlashRedelegation(cachedCtx, validator, r, 0, math.LegacyNewDec(1))
		redelegationsInTokens = redelegationsInTokens.Add(amountSlashed)
	}

	// The power we pass to staking's keeper `Slash` method is the current power of the validator together with the total
	// power of all the currently undelegated and redelegated tokens (see docs/docs/adrs/adr-013-equivocation-slashing.md).
	undelegationsAndRedelegationsInPower := sdk.TokensToConsensusPower(
		undelegationsInTokens.Add(redelegationsInTokens), powerReduction)

	return power + undelegationsAndRedelegationsInPower
}

// slashableStake looks up the validator behind providerAddr and computes the
// consensus power -- and its token equivalent -- that would be slashed for
// it, folding in the power currently tied up in non-matured undelegations
// and redelegations (see ComputePowerToSlash). Returns
// ErrNoValidatorForAddress / ErrBadValidatorAddr / ErrNoUnbondingDelegation /
// ErrValidatorTombstoned under the same conditions SlashValidator has always
// rejected under; callers that only care about rejecting those conditions
// (rather than computing a fraction from tokens) can keep using
// SlashValidator directly.
func (k Keeper) slashableStake(ctx sdk.Context, providerAddr types.ProviderConsAddress) (
	totalPower int64,
	totalTokens math.Int,
	consAddr sdk.ConsAddress,
	err error,
) {
	validator, err := k.stakingKeeper.GetValidatorByConsAddr(ctx, providerAddr.ToSdkConsAddr())
	if err != nil && errors.Is(err, stakingtypes.ErrNoValidatorFound) {
		return 0, totalTokens, consAddr, errorsmod.Wrapf(slashingtypes.ErrNoValidatorForAddress, "provider consensus address: %s", providerAddr.String())
	} else if err != nil {
		return 0, totalTokens, consAddr, errorsmod.Wrapf(slashingtypes.ErrBadValidatorAddr, "unknown error looking for provider consensus address: %s", providerAddr.String())
	}

	if validator.IsUnbonded() {
		return 0, totalTokens, consAddr, errorsmod.Wrapf(stakingtypes.ErrNoUnbondingDelegation, "validator is unbonded. provider consensus address: %s", providerAddr.String())
	}

	if k.slashingKeeper.IsTombstoned(ctx, providerAddr.ToSdkConsAddr()) {
		return 0, totalTokens, consAddr, errorsmod.Wrapf(slashingtypes.ErrValidatorTombstoned, "validator is tombstoned. provider consensus address: %s", providerAddr.String())
	}

	valAddr, err := k.ValidatorAddressCodec().StringToBytes(validator.GetOperator())
	if err != nil {
		return 0, totalTokens, consAddr, err
	}

	undelegations, err := k.stakingKeeper.GetUnbondingDelegationsFromValidator(ctx, valAddr)
	if err != nil {
		return 0, totalTokens, consAddr, err
	}
	redelegations, err := k.stakingKeeper.GetRedelegationsFromSrcValidator(ctx, valAddr)
	if err != nil {
		return 0, totalTokens, consAddr, err
	}
	lastPower, err := k.stakingKeeper.GetLastValidatorPower(ctx, valAddr)
	if err != nil {
		return 0, totalTokens, consAddr, err
	}

	powerReduction := k.stakingKeeper.PowerReduction(ctx)
	totalPower = k.ComputePowerToSlash(ctx, validator, undelegations, redelegations, lastPower, powerReduction)
	totalTokens = sdk.TokensFromConsensusPower(totalPower, powerReduction)

	consAddr, err = validator.GetConsAddr()
	if err != nil {
		return totalPower, totalTokens, consAddr, err
	}

	return totalPower, totalTokens, consAddr, nil
}

// SlashValidator slashes validator with given provider Address
func (k Keeper) SlashValidator(ctx sdk.Context, providerAddr types.ProviderConsAddress, slashingParams *types.SlashJailParameters, infraction stakingtypes.Infraction) error {
	totalPower, _, consAddr, err := k.slashableStake(ctx, providerAddr)
	if err != nil {
		return err
	}

	_, err = k.stakingKeeper.SlashWithInfractionReason(ctx, consAddr, 0, totalPower, slashingParams.SlashFraction, infraction)
	return err
}
