package keeper

import (
	"time"

	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	sdk "github.com/cosmos/cosmos-sdk/types"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
)

// OverrideWindowEndTimestampForTest replaces the downtime-evidence window-end
// timestamp resolution with fn. Production code always uses the real
// IBC-client-backed implementation wired by NewKeeper (see
// Keeper.windowEndTimestamp); this exists solely so unit tests can supply an
// anchor timestamp without fabricating real IBC consensus states.
func (k *Keeper) OverrideWindowEndTimestampForTest(fn func(ctx sdk.Context, clientId string, windowEnd int64) (time.Time, error)) {
	k.windowEndTimestampFn = fn
}

// OverrideVerifyDowntimeChallengeHeaderForTest replaces the downtime
// challenge header light-client verification with fn. Production code always
// uses the real 07-tendermint light client module (see
// Keeper.verifyDowntimeChallengeHeader); this exists solely so unit tests can
// bypass fabricating a real IBC client store.
func (k *Keeper) OverrideVerifyDowntimeChallengeHeaderForTest(fn func(ctx sdk.Context, clientId string, header *ibctmtypes.Header) error) {
	k.verifyDowntimeChallengeHeaderFn = fn
}

// WindowEndTimestampForTest exposes windowEndTimestamp for tests exercising
// the real IBC-client-backed anchor resolution -- i.e. with
// windowEndTimestampFn left unset, unlike OverrideWindowEndTimestampForTest.
// Production code never calls this directly; HandleConsumerDowntime always
// goes through windowEndTimestamp itself.
func (k Keeper) WindowEndTimestampForTest(ctx sdk.Context, clientId string, windowEnd int64) (time.Time, error) {
	return k.windowEndTimestamp(ctx, clientId, windowEnd)
}

// VerifyDowntimeChallengeHeaderForTest exposes verifyDowntimeChallengeHeader
// for tests exercising the real light-client-backed verification path -- i.e.
// with verifyDowntimeChallengeHeaderFn left unset, unlike
// OverrideVerifyDowntimeChallengeHeaderForTest. Production code never calls
// this directly; HandleChallengeConsumerDowntime always goes through
// verifyDowntimeChallengeHeader itself.
func (k Keeper) VerifyDowntimeChallengeHeaderForTest(ctx sdk.Context, clientId string, header *ibctmtypes.Header) error {
	return k.verifyDowntimeChallengeHeader(ctx, clientId, header)
}

// OverrideRemovalVoteForTest replaces the removal-vote lookup with fn.
// Production code always scans the real gov keeper's active-proposal queue
// (see Keeper.removalVote); this exists solely so unit tests can steer the
// punishment deferrals without constructing a real gov keeper.
func (k *Keeper) OverrideRemovalVoteForTest(fn func(ctx sdk.Context, consumerId uint64) (uint64, time.Time, bool)) {
	k.removalVoteFn = fn
}

// OverrideRemovalProposalStatusForTest replaces the proposal-status lookup
// with fn (see Keeper.removalProposalStatus), so unit tests can decide a
// deferred punishment's fate without a real gov keeper.
func (k *Keeper) OverrideRemovalProposalStatusForTest(fn func(ctx sdk.Context, proposalId uint64) (govv1.ProposalStatus, time.Time, bool)) {
	k.removalProposalStatusFn = fn
}

// HoldUnbondingOpForTest exposes holdUnbondingOp so unit tests can seed
// hook-placed holds without driving the full staking hook resolution.
func (k Keeper) HoldUnbondingOpForTest(ctx sdk.Context, consAddr sdk.ConsAddress, id uint64) error {
	return k.holdUnbondingOp(ctx, consAddr, id)
}
