package keeper_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
)

// refusalValAddr builds a deterministic operator address for tests.
func refusalValAddr(seed byte) sdk.ValAddress {
	addr := make([]byte, 20)
	for i := range addr {
		addr[i] = seed
	}
	return sdk.ValAddress(addr)
}

// TestRecordConsumerRefusalRoundTrip covers set, read-back, and withdraw.
func TestRecordConsumerRefusalRoundTrip(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	val := refusalValAddr(1)

	require.False(t, k.HasConsumerRefusal(ctx, cid, val))
	require.NoError(t, k.RecordConsumerRefusal(ctx, cid, val, true))
	require.True(t, k.HasConsumerRefusal(ctx, cid, val))

	addrs, err := k.GetConsumerRefusals(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, []sdk.ValAddress{val}, addrs)

	// Withdrawing is idempotent and leaves no record.
	require.NoError(t, k.RecordConsumerRefusal(ctx, cid, val, false))
	require.NoError(t, k.RecordConsumerRefusal(ctx, cid, val, false))
	require.False(t, k.HasConsumerRefusal(ctx, cid, val))
}

// TestEvaluateConsumerRefusalsPausesAtThreshold proves the threshold pause:
// below the default one-third nothing happens, at it the consumer is paused
// through the standard pause machinery (phase, expiration, auto-stop queue).
func TestEvaluateConsumerRefusalsPausesAtThreshold(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(time.Now())

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)

	val := refusalValAddr(2)
	require.NoError(t, k.RecordConsumerRefusal(ctx, cid, val, true))

	// 30 of 100 bonded power refuses: below one third, stays launched.
	mocks.MockStakingKeeper.EXPECT().GetLastValidatorPower(gomock.Any(), val).Return(int64(30), nil)
	mocks.MockStakingKeeper.EXPECT().GetLastTotalPower(gomock.Any()).Return(math.NewInt(100), nil)
	k.EvaluateConsumerRefusals(ctx)
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cid))

	// 34 of 100: over one third, paused with the auto-stop scheduled.
	mocks.MockStakingKeeper.EXPECT().GetLastValidatorPower(gomock.Any(), val).Return(int64(34), nil)
	mocks.MockStakingKeeper.EXPECT().GetLastTotalPower(gomock.Any()).Return(math.NewInt(100), nil)
	k.EvaluateConsumerRefusals(ctx)
	require.Equal(t, providertypes.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))

	wantExpiration := ctx.BlockTime().Add(k.GetMaxPauseDuration(ctx))
	gotExpiration, err := k.GetConsumerPauseExpirationTime(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, wantExpiration, gotExpiration)

	// A paused consumer is skipped on the next evaluation: no power reads.
	k.EvaluateConsumerRefusals(ctx)
	require.Equal(t, providertypes.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))
}

// TestEvaluateConsumerRefusalsIgnoresPowerlessSignals proves a signal from a
// validator with no bonded power contributes zero instead of failing the
// evaluation.
func TestEvaluateConsumerRefusalsIgnoresPowerlessSignals(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(time.Now())

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)

	gone := refusalValAddr(3)
	require.NoError(t, k.RecordConsumerRefusal(ctx, cid, gone, true))

	// x/staking reports zero, not an error, for a validator outside the set.
	// A powerless refuser x/staking no longer knows at all has its signal
	// pruned; one that merely lost its power keeps it.
	jailed := refusalValAddr(4)
	require.NoError(t, k.RecordConsumerRefusal(ctx, cid, jailed, true))
	mocks.MockStakingKeeper.EXPECT().GetLastValidatorPower(gomock.Any(), gone).Return(int64(0), nil)
	mocks.MockStakingKeeper.EXPECT().GetLastValidatorPower(gomock.Any(), jailed).Return(int64(0), nil)
	mocks.MockStakingKeeper.EXPECT().GetLastTotalPower(gomock.Any()).Return(math.NewInt(100), nil)
	mocks.MockStakingKeeper.EXPECT().GetValidator(gomock.Any(), gone).Return(stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound)
	mocks.MockStakingKeeper.EXPECT().GetValidator(gomock.Any(), jailed).Return(stakingtypes.Validator{Jailed: true}, nil)

	k.EvaluateConsumerRefusals(ctx)
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cid))
	require.False(t, k.HasConsumerRefusal(ctx, cid, gone), "a removed validator's signal is pruned")
	require.True(t, k.HasConsumerRefusal(ctx, cid, jailed), "a powerless but existing validator keeps its signal")
}

// TestSetConsumerRefusalMsgAuthorization pins the handler's authorization: the
// signer must be the validator's operator account, the validator must be
// bonded, and the consumer must be launched or paused.
func TestSetConsumerRefusalMsgAuthorization(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)

	val := refusalValAddr(4)
	operator := sdk.AccAddress(val)
	msgServer := providerkeeper.NewMsgServerImpl(&k)

	// Wrong signer.
	_, err := msgServer.SetConsumerRefusal(ctx, providertypes.NewMsgSetConsumerRefusal(
		sdk.AccAddress(refusalValAddr(5)).String(), cid, val.String(), true))
	require.ErrorIs(t, err, providertypes.ErrUnauthorized)

	// Unknown validator.
	mocks.MockStakingKeeper.EXPECT().GetValidator(gomock.Any(), val).
		Return(stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound)
	_, err = msgServer.SetConsumerRefusal(ctx, providertypes.NewMsgSetConsumerRefusal(
		operator.String(), cid, val.String(), true))
	require.ErrorIs(t, err, stakingtypes.ErrNoValidatorFound)

	// Unbonded validator.
	mocks.MockStakingKeeper.EXPECT().GetValidator(gomock.Any(), val).
		Return(stakingtypes.Validator{Status: stakingtypes.Unbonded}, nil)
	_, err = msgServer.SetConsumerRefusal(ctx, providertypes.NewMsgSetConsumerRefusal(
		operator.String(), cid, val.String(), true))
	require.Error(t, err)
	require.ErrorIs(t, err, sdkerrors.ErrInvalidRequest)

	// Happy path: bonded validator, launched consumer.
	mocks.MockStakingKeeper.EXPECT().GetValidator(gomock.Any(), val).
		Return(stakingtypes.Validator{Status: stakingtypes.Bonded}, nil)
	_, err = msgServer.SetConsumerRefusal(ctx, providertypes.NewMsgSetConsumerRefusal(
		operator.String(), cid, val.String(), true))
	require.NoError(t, err)
	require.True(t, k.HasConsumerRefusal(ctx, cid, val))

	// Phase gate: a registered-only consumer rejects refusals, a paused one
	// takes them, a stopped one rejects them.
	for _, tc := range []struct {
		phase providertypes.ConsumerPhase
		ok    bool
	}{
		{providertypes.CONSUMER_PHASE_REGISTERED, false},
		{providertypes.CONSUMER_PHASE_PAUSED, true},
		{providertypes.CONSUMER_PHASE_STOPPED, false},
	} {
		cid2 := k.FetchAndIncrementConsumerId(ctx)
		k.SetConsumerPhase(ctx, cid2, tc.phase)
		mocks.MockStakingKeeper.EXPECT().GetValidator(gomock.Any(), val).
			Return(stakingtypes.Validator{Status: stakingtypes.Bonded}, nil)
		_, err = msgServer.SetConsumerRefusal(ctx, providertypes.NewMsgSetConsumerRefusal(
			operator.String(), cid2, val.String(), true))
		if tc.ok {
			require.NoError(t, err, tc.phase)
		} else {
			require.ErrorIs(t, err, providertypes.ErrInvalidPhase, tc.phase)
		}
	}

	// Withdrawing needs neither a bonded validator nor a running consumer:
	// no validator lookup, and the stopped consumer's record goes away.
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_STOPPED)
	_, err = msgServer.SetConsumerRefusal(ctx, providertypes.NewMsgSetConsumerRefusal(
		operator.String(), cid, val.String(), false))
	require.NoError(t, err)
	require.False(t, k.HasConsumerRefusal(ctx, cid, val))
}

// TestDeleteConsumerRefusalsScopedToConsumer proves deletion touches only the
// target consumer's signals.
func TestDeleteConsumerRefusalsScopedToConsumer(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	val := refusalValAddr(7)
	cidA := k.FetchAndIncrementConsumerId(ctx)
	cidB := k.FetchAndIncrementConsumerId(ctx)
	require.NoError(t, k.RecordConsumerRefusal(ctx, cidA, val, true))
	require.NoError(t, k.RecordConsumerRefusal(ctx, cidB, val, true))

	require.NoError(t, k.DeleteConsumerRefusals(ctx, cidA))
	require.False(t, k.HasConsumerRefusal(ctx, cidA, val))
	require.True(t, k.HasConsumerRefusal(ctx, cidB, val))
}

// TestEvaluateConsumerRefusalsPausesInConsumerIdOrder: two consumers crossing
// the threshold in one block land in the same auto-stop bucket, whose
// contents must be identical on every node, so consumers are visited in key
// order. Both pause and the bucket lists them ascending.
func TestEvaluateConsumerRefusalsPausesInConsumerIdOrder(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(time.Now())

	val := refusalValAddr(6)
	var cids []uint64
	for i := 0; i < 3; i++ {
		cid := k.FetchAndIncrementConsumerId(ctx)
		k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
		require.NoError(t, k.RecordConsumerRefusal(ctx, cid, val, true))
		cids = append(cids, cid)
	}
	mocks.MockStakingKeeper.EXPECT().GetLastValidatorPower(gomock.Any(), val).Return(int64(50), nil).Times(3)
	mocks.MockStakingKeeper.EXPECT().GetLastTotalPower(gomock.Any()).Return(math.NewInt(100), nil).Times(3)

	k.EvaluateConsumerRefusals(ctx)

	for _, cid := range cids {
		require.Equal(t, providertypes.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))
	}
	bucket, err := k.GetConsumersToBeAutoStopped(ctx, ctx.BlockTime().Add(k.GetMaxPauseDuration(ctx)))
	require.NoError(t, err)
	require.Equal(t, cids, bucket.Ids, "the shared auto-stop bucket is filled in key order")
}

// TestEvaluateConsumerRefusalsThresholdEdges pins the boundary: exactly one
// third of the power (1 of 3) reaches the default threshold, and a chain with
// nothing bonded is never paused by a signal.
func TestEvaluateConsumerRefusalsThresholdEdges(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(time.Now())

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
	val := refusalValAddr(7)
	require.NoError(t, k.RecordConsumerRefusal(ctx, cid, val, true))

	mocks.MockStakingKeeper.EXPECT().GetLastValidatorPower(gomock.Any(), val).Return(int64(0), nil)
	mocks.MockStakingKeeper.EXPECT().GetLastTotalPower(gomock.Any()).Return(math.ZeroInt(), nil)
	mocks.MockStakingKeeper.EXPECT().GetValidator(gomock.Any(), val).Return(stakingtypes.Validator{}, nil)
	k.EvaluateConsumerRefusals(ctx)
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cid), "nothing bonded, nothing refused")

	mocks.MockStakingKeeper.EXPECT().GetLastValidatorPower(gomock.Any(), val).Return(int64(1), nil)
	mocks.MockStakingKeeper.EXPECT().GetLastTotalPower(gomock.Any()).Return(math.NewInt(3), nil)
	k.EvaluateConsumerRefusals(ctx)
	require.Equal(t, providertypes.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid), "one third reaches the threshold")

	var reached bool
	for _, ev := range ctx.EventManager().Events() {
		reached = reached || ev.Type == providertypes.EventTypeConsumerRefusalThresholdReached
	}
	require.True(t, reached, "the pause names its cause")
}

// TestResumeConsumerRefusedWhileCoalitionStands: a governance resume against
// a coalition still at the threshold is refused outright; once the coalition
// withdraws the resume proceeds past the refusal check.
func TestResumeConsumerRefusedWhileCoalitionStands(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(time.Now())

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_PAUSED)
	val := refusalValAddr(8)
	require.NoError(t, k.RecordConsumerRefusal(ctx, cid, val, true))

	mocks.MockStakingKeeper.EXPECT().GetLastValidatorPower(gomock.Any(), val).Return(int64(40), nil)
	mocks.MockStakingKeeper.EXPECT().GetLastTotalPower(gomock.Any()).Return(math.NewInt(100), nil)
	err := k.ResumeConsumerChain(ctx, cid)
	require.ErrorIs(t, err, providertypes.ErrConsumerRefused)
	require.Equal(t, providertypes.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))

	// Withdrawn: the resume gets past the refusal check without touching
	// x/staking (and fails further on, at the client this bare keeper has
	// none of).
	require.NoError(t, k.RecordConsumerRefusal(ctx, cid, val, false))
	err = k.ResumeConsumerChain(ctx, cid)
	require.ErrorIs(t, err, providertypes.ErrInvalidConsumerClient)
}

// TestDeleteConsumerChainClearsRefusals: deleting a stopped consumer erases
// its refusal records with the rest of its state.
func TestDeleteConsumerChainClearsRefusals(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerClientId(ctx, cid, "07-tendermint-0")
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_STOPPED)
	val := refusalValAddr(9)
	require.NoError(t, k.RecordConsumerRefusal(ctx, cid, val, true))
	// Deletion sweeps the (empty) fee pool on its way.
	mocks.MockBankKeeper.EXPECT().GetAllBalances(gomock.Any(), gomock.Any()).Return(sdk.NewCoins()).AnyTimes()

	require.NoError(t, k.DeleteConsumerChain(ctx, cid))
	require.False(t, k.HasConsumerRefusal(ctx, cid, val))
}

// TestQueryConsumerRefusals covers the query: an unknown consumer is an
// invalid argument, a known one reports its refusers and the refused share.
func TestQueryConsumerRefusals(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	_, err := k.QueryConsumerRefusals(ctx, &providertypes.QueryConsumerRefusalsRequest{ConsumerId: 99})
	require.Error(t, err)

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerChainId(ctx, cid, "refused-1")
	val := refusalValAddr(10)
	require.NoError(t, k.RecordConsumerRefusal(ctx, cid, val, true))
	mocks.MockStakingKeeper.EXPECT().GetLastValidatorPower(gomock.Any(), val).Return(int64(25), nil)
	mocks.MockStakingKeeper.EXPECT().GetLastTotalPower(gomock.Any()).Return(math.NewInt(100), nil)

	res, err := k.QueryConsumerRefusals(ctx, &providertypes.QueryConsumerRefusalsRequest{ConsumerId: cid})
	require.NoError(t, err)
	require.Equal(t, []string{val.String()}, res.ValidatorAddresses)
	require.Equal(t, int64(25), res.RefusedPower)
	require.Equal(t, int64(100), res.TotalPower)
	require.Equal(t, math.LegacyNewDecWithPrec(25, 2).String(), res.RefusedFraction)
	require.Equal(t, k.GetParams(ctx).RefusalPauseThreshold, res.PauseThreshold)
}
