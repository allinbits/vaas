package keeper_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/codec/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// TestRefusalPauseFreezesDowntimeAccusations: a pause at the refusal
// threshold keeps the consumer's downtime accusations instead of discarding
// them. Nothing is repaid (the bank mock carries no expectations, so the
// controller proves no payment was attempted), the pending slash, its
// withheld fee record and the epoch mark stay, neither the maturity nor the
// expiry runs while paused, and a resume moves both forward by the length of
// the pause.
func TestRefusalPauseFreezesDowntimeAccusations(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC))
	k.SetInfractionParams(ctx, types.DefaultInfractionParameters())

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerClientId(ctx, cid, "07-tendermint-0")
	k.SetConsumerPhase(ctx, cid, types.CONSUMER_PHASE_LAUNCHED)
	providerAddr := types.NewProviderConsAddress(sdk.ConsAddress([]byte("validator-address-1")))
	consAddr := providerAddr.ToSdkConsAddr()
	maturesAt := ctx.BlockTime().Add(time.Hour)
	expiresAt := ctx.BlockTime().Add(2 * time.Hour)
	putPendingDowntimeSlash(t, k, ctx, cid, providerAddr, math.NewInt(100), maturesAt, 100)
	putWithheldFeeRecord(t, k, ctx, cid, consAddr, sdk.NewInt64Coin("uphoton", 500), expiresAt)
	k.MarkEpochDowntime(ctx, cid, consAddr)
	slashKey := collections.Join3(cid, consAddr.Bytes(), int64(100))
	recordKey := collections.Join(cid, consAddr.Bytes())

	require.NoError(t, k.PauseConsumerChain(ctx, cid, types.PAUSE_REASON_REFUSAL_THRESHOLD))
	require.Equal(t, types.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))
	pending, err := k.PendingDowntimeSlashes.Get(ctx, slashKey)
	require.NoError(t, err, "the refusal pause keeps the pending slash")
	require.Equal(t, maturesAt, pending.MaturesAt)
	record, err := k.WithheldFeeRecords.Get(ctx, recordKey)
	require.NoError(t, err, "the refusal pause keeps the withheld fee record")
	require.Equal(t, expiresAt, record.ExpiresAt)
	require.True(t, k.IsEpochDowntime(ctx, cid, consAddr), "the refusal pause keeps the epoch mark")

	// Frozen: well past both the maturity and the expiry, the sweeps leave
	// the accusations alone (no staking expectations exist for an execution).
	frozen := ctx.WithBlockTime(expiresAt.Add(time.Hour))
	k.SweepPendingDowntimeSlashes(frozen)
	k.SweepExpiredWithheldFeeRecords(frozen)
	_, err = k.PendingDowntimeSlashes.Get(frozen, slashKey)
	require.NoError(t, err, "a frozen slash does not mature")
	_, err = k.WithheldFeeRecords.Get(frozen, recordKey)
	require.NoError(t, err, "a frozen record does not expire")

	// Resumed after three hours: the slash matures, and the record expires,
	// three hours later than they would have.
	pausedFor := 3 * time.Hour
	resumed := ctx.WithBlockTime(ctx.BlockTime().Add(pausedFor))
	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), "07-tendermint-0").Return(ibcexported.Active)
	resumeMocks(mocks)
	require.NoError(t, k.ResumeConsumerChain(resumed, cid))
	pending, err = k.PendingDowntimeSlashes.Get(resumed, slashKey)
	require.NoError(t, err)
	require.Equal(t, maturesAt.Add(pausedFor), pending.MaturesAt)
	record, err = k.WithheldFeeRecords.Get(resumed, recordKey)
	require.NoError(t, err)
	require.Equal(t, expiresAt.Add(pausedFor), record.ExpiresAt)
}

// TestRefusalPauseThatLapsesCancelsTheFrozenAccusations: a refusal pause
// governance never resolves auto-stops like any other, and the stop cancels
// the frozen accusations the way every stop does.
func TestRefusalPauseThatLapsesCancelsTheFrozenAccusations(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC))
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).AnyTimes()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, types.CONSUMER_PHASE_LAUNCHED)
	providerAddr := types.NewProviderConsAddress(sdk.ConsAddress([]byte("validator-address-1")))
	putPendingDowntimeSlash(t, k, ctx, cid, providerAddr, math.NewInt(100), ctx.BlockTime().Add(time.Hour), 100)
	k.MarkEpochDowntime(ctx, cid, providerAddr.ToSdkConsAddr())
	require.NoError(t, k.PauseConsumerChain(ctx, cid, types.PAUSE_REASON_REFUSAL_THRESHOLD))

	lapsed := ctx.WithBlockTime(ctx.BlockTime().Add(k.GetMaxPauseDuration(ctx) + time.Second))
	require.NoError(t, k.BeginBlockAutoStopPausedConsumers(lapsed))
	require.Equal(t, types.CONSUMER_PHASE_STOPPED, k.GetConsumerPhase(lapsed, cid))
	_, err := k.PendingDowntimeSlashes.Get(lapsed, collections.Join3(cid, providerAddr.ToSdkConsAddr().Bytes(), int64(100)))
	require.ErrorIs(t, err, collections.ErrNotFound, "the stop cancels the frozen slash")
	require.False(t, k.IsEpochDowntime(lapsed, cid, providerAddr.ToSdkConsAddr()))
}

// TestChallengeDuringRefusalPauseDiscardsTheAccusations: a validator can
// still disprove a frozen accusation while the consumer sits in a refusal
// pause. The proof discards the consumer's accusations and repays the
// withheld fees exactly as it would on a launched consumer, and the pause
// goes on under its own reason: nothing is paused twice.
func TestChallengeDuringRefusalPauseDiscardsTheAccusations(t *testing.T) {
	f := newChallengeFixture(t)
	require.NoError(t, f.k.PauseConsumerChain(f.ctx, f.cid, types.PAUSE_REASON_REFUSAL_THRESHOLD))
	_, err := f.k.PendingDowntimeSlashes.Get(f.ctx, f.pendingSlashKey())
	require.NoError(t, err, "the refusal pause froze the accusation")

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	f.mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()
	val, valBytes := newBondedValidator(t, valAddrCodec, 7)
	f.mocks.MockStakingKeeper.EXPECT().
		GetValidatorByConsAddr(gomock.Any(), f.providerAddr.ToSdkConsAddr()).
		Return(val, nil)
	poolAddr := f.k.GetConsumerFeePoolAddress(f.cid)
	amount := sdk.NewInt64Coin("uphoton", 500)
	f.mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), poolAddr, "uphoton").
		Return(sdk.NewInt64Coin("uphoton", 1000))
	f.mocks.MockBankKeeper.EXPECT().
		InputOutputCoins(gomock.Any(),
			banktypes.Input{Address: poolAddr.String(), Coins: sdk.NewCoins(amount)},
			[]banktypes.Output{{Address: accAddr(valBytes), Coins: sdk.NewCoins(amount)}},
		).Return(nil)

	require.NoError(t, f.k.HandleChallengeConsumerDowntime(f.ctx, f.msg))

	_, err = f.k.PendingDowntimeSlashes.Get(f.ctx, f.pendingSlashKey())
	require.ErrorIs(t, err, collections.ErrNotFound, "the proof discards the frozen slash")
	has, err := f.k.WithheldFeeRecords.Has(f.ctx, f.pairKey())
	require.NoError(t, err)
	require.False(t, has, "the withheld fee record is paid out and deleted")
	require.Equal(t, types.CONSUMER_PHASE_PAUSED, f.k.GetConsumerPhase(f.ctx, f.cid))
	require.Equal(t, types.PAUSE_REASON_REFUSAL_THRESHOLD, f.k.GetConsumerPauseReason(f.ctx, f.cid), "the pause keeps its reason")
	require.Equal(t, 1, countEvents(f.ctx, vaastypes.EventTypeConsumerPaused), "paused once, by the refusal")
	require.Equal(t, 1, countEvents(f.ctx, vaastypes.EventTypeDowntimeChallengeSucceeded))
}

// TestChallengeDuringRefusalPauseRepaysARecordWhoseExpiryPassedMeanwhile:
// the freeze stops the record's clock, so a proof landing after the record's
// recorded expiry, but during the pause, still finds it claimable and repays
// it. The expiry is measured against the moment the pause began.
func TestChallengeDuringRefusalPauseRepaysARecordWhoseExpiryPassedMeanwhile(t *testing.T) {
	f := newChallengeFixture(t)
	require.NoError(t, f.k.PauseConsumerChain(f.ctx, f.cid, types.PAUSE_REASON_REFUSAL_THRESHOLD))
	record, err := f.k.WithheldFeeRecords.Get(f.ctx, f.pairKey())
	require.NoError(t, err)
	later := f.ctx.WithBlockTime(record.ExpiresAt.Add(time.Hour))

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	f.mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()
	val, valBytes := newBondedValidator(t, valAddrCodec, 7)
	f.mocks.MockStakingKeeper.EXPECT().
		GetValidatorByConsAddr(gomock.Any(), f.providerAddr.ToSdkConsAddr()).
		Return(val, nil)
	poolAddr := f.k.GetConsumerFeePoolAddress(f.cid)
	f.mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), poolAddr, "uphoton").
		Return(sdk.NewInt64Coin("uphoton", 1000))
	f.mocks.MockBankKeeper.EXPECT().
		InputOutputCoins(gomock.Any(),
			banktypes.Input{Address: poolAddr.String(), Coins: sdk.NewCoins(record.Amount)},
			[]banktypes.Output{{Address: accAddr(valBytes), Coins: sdk.NewCoins(record.Amount)}},
		).Return(nil)

	require.NoError(t, f.k.HandleChallengeConsumerDowntime(later, f.msg))
	has, err := f.k.WithheldFeeRecords.Has(later, f.pairKey())
	require.NoError(t, err)
	require.False(t, has, "the frozen record is repaid, not skipped as expired")
}

// TestWithheldEscrowStaysReservedWhilePaused: the escrow behind a frozen
// record keeps reserving the pool balance past the record's recorded expiry
// for as long as the consumer is paused, so a depositor's claim is quoted,
// and paid, net of it.
func TestWithheldEscrowStaysReservedWhilePaused(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC))

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, types.CONSUMER_PHASE_LAUNCHED)
	alice := sdk.AccAddress([]byte("alice___________"))
	poolAddr := k.GetConsumerFeePoolAddress(cid)
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx, collections.Join3(cid, "uphoton", alice), math.NewInt(100)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx, collections.Join(cid, "uphoton"), math.NewInt(100)))
	putWithheldFeeRecord(t, k, ctx, cid, []byte("val-escrow-holder__"), sdk.NewInt64Coin("uphoton", 40), ctx.BlockTime().Add(time.Hour))
	require.NoError(t, k.PauseConsumerChain(ctx, cid, types.PAUSE_REASON_REFUSAL_THRESHOLD))

	later := ctx.WithBlockTime(ctx.BlockTime().Add(2 * time.Hour))
	mocks.MockBankKeeper.EXPECT().GetBalance(later, poolAddr, "uphoton").Return(sdk.NewInt64Coin("uphoton", 100)).AnyTimes()
	res, err := k.ConsumerFeePoolClaim(later, &types.QueryConsumerFeePoolClaimRequest{ConsumerId: cid, Depositor: alice.String()})
	require.NoError(t, err)
	require.Equal(t, "60uphoton", res.Claim.String(), "the frozen record still reserves its escrow")
}

// TestChallengeAgainstAConsumerPausedForAnotherReasonIsRefused: only a
// refusal pause leaves accusations frozen behind it, so a proven challenge
// finds something to discard on a paused consumer only there. A pending
// slash under any other pause is a state the pauses never produce, and the
// handler refuses to act on it rather than guess.
func TestChallengeAgainstAConsumerPausedForAnotherReasonIsRefused(t *testing.T) {
	f := newChallengeFixture(t)
	f.k.SetConsumerPhase(f.ctx, f.cid, types.CONSUMER_PHASE_PAUSED)
	require.NoError(t, f.k.SetConsumerPauseReason(f.ctx, f.cid, types.PAUSE_REASON_DOWNTIME_CHALLENGE))
	require.NoError(t, f.k.SetConsumerPausedAt(f.ctx, f.cid, f.ctx.BlockTime()))

	err := f.k.HandleChallengeConsumerDowntime(f.ctx, f.msg)
	require.ErrorIs(t, err, types.ErrInvalidPhase)
	_, err = f.k.PendingDowntimeSlashes.Get(f.ctx, f.pendingSlashKey())
	require.NoError(t, err, "nothing is discarded")
	has, err := f.k.WithheldFeeRecords.Has(f.ctx, f.pairKey())
	require.NoError(t, err)
	require.True(t, has, "nothing is repaid")
}
