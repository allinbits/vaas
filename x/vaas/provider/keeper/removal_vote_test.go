package keeper_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	govkeeper "github.com/cosmos/cosmos-sdk/x/gov/keeper"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	"github.com/allinbits/vaas/x/vaas/provider/types"
)

// setupRemovalVoteScanTest wires a real gov keeper into the provider keeper so
// the removal-vote scan runs against gov's own proposal store and
// active-proposals queue, exactly as in production, rather than through the
// test override.
func setupRemovalVoteScanTest(t *testing.T) (
	providerkeeper.Keeper, sdk.Context, *gomock.Controller, testkeeper.MockedKeepers, *govkeeper.Keeper,
	stakingtypes.Validator, types.ProviderConsAddress,
) {
	t.Helper()
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	ctx = ctx.WithBlockTime(time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC))
	k.SetInfractionParams(ctx, types.InfractionParameters{
		Downtime: &types.SlashJailParameters{SlashFraction: math.LegacyNewDecWithPrec(5, 1)},
	})
	gk := testkeeper.NewInMemGovKeeper(t, params)
	k.SetGovKeeper(gk)

	validator, providerAddr := newSweepValidator(t)
	return k, ctx, ctrl, mocks, gk, validator, providerAddr
}

// seedGovProposal stores a proposal carrying msgs in gov's collections in the
// given status: a voting-period proposal goes into the active queue keyed by
// votingEnd, a deposit-period one into the inactive queue.
func seedGovProposal(t *testing.T, gk *govkeeper.Keeper, ctx sdk.Context, id uint64, msgs []sdk.Msg, status govv1.ProposalStatus, votingEnd time.Time) {
	t.Helper()
	proposer := sdk.AccAddress(bytes.Repeat([]byte{0x01}, 20))
	depositEnd := ctx.BlockTime().Add(time.Hour)
	proposal, err := govv1.NewProposal(msgs, id, ctx.BlockTime(), depositEnd, "", "remove", "remove", proposer)
	require.NoError(t, err)
	proposal.Status = status
	switch status {
	case govv1.StatusVotingPeriod:
		start := ctx.BlockTime()
		proposal.VotingStartTime = &start
		proposal.VotingEndTime = &votingEnd
		require.NoError(t, gk.SetProposal(ctx, proposal))
		require.NoError(t, gk.ActiveProposalsQueue.Set(ctx, collections.Join(votingEnd, id), id))
	case govv1.StatusDepositPeriod:
		require.NoError(t, gk.SetProposal(ctx, proposal))
		require.NoError(t, gk.InactiveProposalsQueue.Set(ctx, collections.Join(depositEnd, id), id))
	default:
		require.NoError(t, gk.SetProposal(ctx, proposal))
	}
}

// TestRemovalVoteScanDefersBehindLiveRemovalProposal proves the production
// scan: a MsgRemoveConsumer proposal for the consumer in its voting period,
// found by walking gov's active-proposals queue, defers a matured downtime
// slash past the voting end.
func TestRemovalVoteScanDefersBehindLiveRemovalProposal(t *testing.T) {
	k, ctx, ctrl, _, gk, _, providerAddr := setupRemovalVoteScanTest(t)
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, types.CONSUMER_PHASE_LAUNCHED)
	putPendingDowntimeSlash(t, k, ctx, cid, providerAddr, math.NewInt(100), ctx.BlockTime().Add(-time.Minute), 100)

	votingEnd := ctx.BlockTime().Add(2 * time.Hour)
	seedGovProposal(t, gk, ctx, 1, []sdk.Msg{
		&types.MsgRemoveConsumer{Authority: k.GetAuthority(), ConsumerId: cid},
	}, govv1.StatusVotingPeriod, votingEnd)

	// No slashing expectations are set: the controller proves the entry was
	// deferred rather than executed.
	k.SweepPendingDowntimeSlashes(ctx)

	entry, err := k.PendingDowntimeSlashes.Get(ctx, collections.Join3(cid, providerAddr.ToSdkConsAddr().Bytes(), int64(100)))
	require.NoError(t, err, "the entry must still be pending")
	require.True(t, entry.MaturesAtExtended)
	require.Equal(t, votingEnd.Add(k.GetParams(ctx).RemovalVoteDeferralMargin), entry.MaturesAt)
}

// TestRemovalVoteScanIgnoresProposalsThatDoNotRemoveTheConsumer pins what the
// scan must not match: a removal of another consumer, a different message for
// this consumer, and a removal of this consumer still in its deposit period.
// With only those on record the matured slash executes on the spot.
func TestRemovalVoteScanIgnoresProposalsThatDoNotRemoveTheConsumer(t *testing.T) {
	k, ctx, ctrl, mocks, gk, validator, providerAddr := setupRemovalVoteScanTest(t)
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	otherCid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, types.CONSUMER_PHASE_LAUNCHED)
	consAddr := providerAddr.ToSdkConsAddr()
	putPendingDowntimeSlash(t, k, ctx, cid, providerAddr, math.NewInt(100), ctx.BlockTime().Add(-time.Minute), 100)

	votingEnd := ctx.BlockTime().Add(2 * time.Hour)
	seedGovProposal(t, gk, ctx, 1, []sdk.Msg{
		&types.MsgRemoveConsumer{Authority: k.GetAuthority(), ConsumerId: otherCid},
	}, govv1.StatusVotingPeriod, votingEnd)
	seedGovProposal(t, gk, ctx, 2, []sdk.Msg{
		&types.MsgResumeConsumer{Authority: k.GetAuthority(), ConsumerId: cid},
	}, govv1.StatusVotingPeriod, votingEnd)
	seedGovProposal(t, gk, ctx, 3, []sdk.Msg{
		&types.MsgRemoveConsumer{Authority: k.GetAuthority(), ConsumerId: cid},
	}, govv1.StatusDepositPeriod, time.Time{})

	powerReduction := math.NewInt(1)
	expectSlashableStakeLookup(t, k, mocks, ctx, validator, consAddr, 1000, powerReduction)
	mocks.MockStakingKeeper.EXPECT().
		SlashWithInfractionReason(ctx, consAddr, int64(0), int64(1000), math.LegacyNewDecWithPrec(1, 1), stakingtypes.Infraction_INFRACTION_DOWNTIME).
		Return(math.NewInt(100), nil)

	k.SweepPendingDowntimeSlashes(ctx)

	has, err := k.PendingDowntimeSlashes.Has(ctx, collections.Join3(cid, consAddr.Bytes(), int64(100)))
	require.NoError(t, err)
	require.False(t, has, "nothing on record shields the consumer, so the slash executes")
}

// TestRemovalProposalStatusReadFromGovState drives the verdict path through
// gov's own proposal store: an equivocation entry deferred behind proposal 5
// is cancelled once gov records that proposal as FAILED with the consumer
// stopped (a passed vote whose removal found nothing to remove), and a
// downtime entry deferred behind proposal 6 keeps waiting while gov still
// shows that proposal in its voting period.
func TestRemovalProposalStatusReadFromGovState(t *testing.T) {
	k, ctx, ctrl, mocks, gk, validator, providerAddr := setupRemovalVoteScanTest(t)
	defer ctrl.Finish()
	consAddr := providerAddr.ToSdkConsAddr()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, types.CONSUMER_PHASE_STOPPED)
	removeMsg := []sdk.Msg{&types.MsgRemoveConsumer{Authority: k.GetAuthority(), ConsumerId: cid}}

	// Equivocation entry deferred behind proposal 5, which gov tallied as
	// passed but could not execute against the stopped consumer.
	seedGovProposal(t, gk, ctx, 5, removeMsg, govv1.StatusFailed, time.Time{})
	eqKey := collections.Join3(cid, consAddr.Bytes(), int64(42))
	require.NoError(t, k.PendingEquivocationPunishments.Set(ctx, eqKey, types.PendingEquivocationPunishment{
		ConsumerId:           cid,
		ProviderConsAddr:     consAddr.Bytes(),
		InfractionHeight:     42,
		ExecutesAt:           ctx.BlockTime().Add(-time.Minute),
		ExecutesAtExtended:   true,
		DeferredByProposalId: 5,
	}))
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx, consAddr).Return(validator, nil)
	mocks.MockSlashingKeeper.EXPECT().JailUntil(ctx, consAddr, ctx.BlockTime())
	k.SweepPendingEquivocationPunishments(ctx)
	has, err := k.PendingEquivocationPunishments.Has(ctx, eqKey)
	require.NoError(t, err)
	require.False(t, has, "gov's FAILED after the stop is a passed vote: the punishment is cancelled")

	// Downtime entry deferred behind proposal 6, still in voting in gov.
	movedEnd := ctx.BlockTime().Add(4 * time.Hour)
	seedGovProposal(t, gk, ctx, 6, removeMsg, govv1.StatusVotingPeriod, movedEnd)
	dtKey := collections.Join3(cid, consAddr.Bytes(), int64(100))
	require.NoError(t, k.PendingDowntimeSlashes.Set(ctx, dtKey, types.PendingDowntimeSlash{
		ConsumerId:           cid,
		ProviderConsAddr:     consAddr.Bytes(),
		Span:                 101,
		SlashTokens:          math.NewInt(100),
		MaturesAt:            ctx.BlockTime().Add(-time.Minute),
		MaturesAtExtended:    true,
		DeferredByProposalId: 6,
	}))
	k.SweepPendingDowntimeSlashes(ctx)
	entry, err := k.PendingDowntimeSlashes.Get(ctx, dtKey)
	require.NoError(t, err, "an open vote in gov keeps the entry pending")
	require.Equal(t, movedEnd.Add(k.GetParams(ctx).RemovalVoteDeferralMargin), entry.MaturesAt)
}

// TestRemovalVoteScanPicksTheLatestEndingVote: with two removal votes for the
// consumer live at once, the entry defers behind the later-ending one, so it
// cannot execute while either is still open.
func TestRemovalVoteScanPicksTheLatestEndingVote(t *testing.T) {
	k, ctx, ctrl, _, gk, _, providerAddr := setupRemovalVoteScanTest(t)
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, types.CONSUMER_PHASE_LAUNCHED)
	putPendingDowntimeSlash(t, k, ctx, cid, providerAddr, math.NewInt(100), ctx.BlockTime().Add(-time.Minute), 100)

	removeMsg := []sdk.Msg{&types.MsgRemoveConsumer{Authority: k.GetAuthority(), ConsumerId: cid}}
	earlyEnd := ctx.BlockTime().Add(time.Hour)
	lateEnd := ctx.BlockTime().Add(3 * time.Hour)
	seedGovProposal(t, gk, ctx, 1, removeMsg, govv1.StatusVotingPeriod, lateEnd)
	seedGovProposal(t, gk, ctx, 2, removeMsg, govv1.StatusVotingPeriod, earlyEnd)

	k.SweepPendingDowntimeSlashes(ctx)

	entry, err := k.PendingDowntimeSlashes.Get(ctx, collections.Join3(cid, providerAddr.ToSdkConsAddr().Bytes(), int64(100)))
	require.NoError(t, err)
	require.Equal(t, uint64(1), entry.DeferredByProposalId, "the later-ending vote is the one waited for")
	require.Equal(t, lateEnd.Add(k.GetParams(ctx).RemovalVoteDeferralMargin), entry.MaturesAt)
}
