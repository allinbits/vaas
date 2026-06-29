package keeper_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	sdk "github.com/cosmos/cosmos-sdk/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
)

func TestConsumerLivenessState(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	const cid = uint64(0)

	// lastAck defaults to BlockTime when absent (defensive, not a migration crutch).
	require.Equal(t, ctx.BlockTime(), k.GetConsumerLastAckTime(ctx, cid))

	now := ctx.BlockTime().Add(time.Hour)
	require.NoError(t, k.SetConsumerLastAckTime(ctx, cid, now))
	require.Equal(t, now.UTC(), k.GetConsumerLastAckTime(ctx, cid).UTC())

	// counters default to 0.
	require.Equal(t, uint64(0), k.GetConsumerHighestSentVscId(ctx, cid))
	require.Equal(t, uint64(0), k.GetConsumerHighestAckedVscId(ctx, cid))
	k.SetConsumerHighestSentVscId(ctx, cid, 5)
	k.SetConsumerHighestAckedVscId(ctx, cid, 3)
	require.Equal(t, uint64(5), k.GetConsumerHighestSentVscId(ctx, cid))
	require.Equal(t, uint64(3), k.GetConsumerHighestAckedVscId(ctx, cid))

	k.DeleteConsumerLastAckTime(ctx, cid)
	k.DeleteConsumerHighestSentVscId(ctx, cid)
	k.DeleteConsumerHighestAckedVscId(ctx, cid)
	require.Equal(t, ctx.BlockTime(), k.GetConsumerLastAckTime(ctx, cid))
	require.Equal(t, uint64(0), k.GetConsumerHighestSentVscId(ctx, cid))
}

func TestOnAckRecordsLiveness(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	const cid = uint64(0)
	clientId := "07-tendermint-0"
	k.SetConsumerClientId(ctx, cid, clientId)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)

	future := ctx.BlockTime().Add(2 * time.Hour)
	ctx = ctx.WithBlockTime(future)

	require.NoError(t, k.OnAcknowledgementPacketV2(ctx, clientId, 9, ""))
	require.Equal(t, future.UTC(), k.GetConsumerLastAckTime(ctx, cid).UTC())
	require.Equal(t, uint64(9), k.GetConsumerHighestAckedVscId(ctx, cid))
}

func TestLivenessGracePeriod(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	unbonding := 21 * 24 * time.Hour
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(unbonding, nil).Times(1)

	grace, err := k.LivenessGracePeriod(ctx)
	require.NoError(t, err)
	require.Greater(t, grace, time.Duration(0))
	require.Less(t, grace, unbonding) // safety invariant
	require.Equal(t, time.Duration(float64(unbonding)*0.66), grace)
}

func TestSweepRemovesStaleConsumer(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerChainId(ctx, cid, "consumer-1")

	unbonding := 21 * 24 * time.Hour
	// LivenessGracePeriod + StopAndPrepareForConsumerRemoval both read UnbondingTime.
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(unbonding, nil).AnyTimes()

	// Last ack far in the past -> beyond grace.
	require.NoError(t, k.SetConsumerLastAckTime(ctx, cid, ctx.BlockTime().Add(-30*24*time.Hour)))

	require.NoError(t, k.SweepUnresponsiveConsumers(ctx))
	require.Equal(t, providertypes.CONSUMER_PHASE_STOPPED, k.GetConsumerPhase(ctx, cid))
}

func TestSweepSparesLiveConsumer(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).AnyTimes()

	require.NoError(t, k.SetConsumerLastAckTime(ctx, cid, ctx.BlockTime())) // fresh
	require.NoError(t, k.SweepUnresponsiveConsumers(ctx))
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cid))
}

func TestTimeoutDoesNotRemove(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	const cid = uint64(0)
	clientId := "07-tendermint-0"
	k.SetConsumerClientId(ctx, cid, clientId)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)

	require.NoError(t, k.OnTimeoutPacketV2(ctx, clientId))
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cid))
}

func TestErrorAckDoesNotRemove(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	const cid = uint64(0)
	clientId := "07-tendermint-0"
	k.SetConsumerClientId(ctx, cid, clientId)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)

	require.NoError(t, k.OnAcknowledgementPacketV2(ctx, clientId, 1, "some error"))
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cid))
}

func TestDeleteConsumerChainClearsLivenessState(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).AnyTimes()

	const cid = uint64(0)
	poolAddr := k.GetConsumerFeePoolAddress(cid)
	k.SetConsumerClientId(ctx, cid, "07-tendermint-0")
	require.NoError(t, k.FeePoolAddressToConsumerId.Set(ctx, poolAddr, cid))
	mocks.MockBankKeeper.EXPECT().GetAllBalances(ctx, poolAddr).Return(sdk.NewCoins())

	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_STOPPED)
	require.NoError(t, k.SetConsumerLastAckTime(ctx, cid, ctx.BlockTime()))
	k.SetConsumerHighestSentVscId(ctx, cid, 4)
	k.SetConsumerHighestAckedVscId(ctx, cid, 4)

	// confirm keys are present before deletion
	hasAck, err := k.ConsumerLastAckTime.Has(ctx, cid)
	require.NoError(t, err)
	require.True(t, hasAck)
	hasSent, err := k.ConsumerHighestSentVscId.Has(ctx, cid)
	require.NoError(t, err)
	require.True(t, hasSent)
	hasAcked, err := k.ConsumerHighestAckedVscId.Has(ctx, cid)
	require.NoError(t, err)
	require.True(t, hasAcked)

	require.NoError(t, k.DeleteConsumerChain(ctx, cid))

	// assert underlying keys are gone, not just that the getter returns a default
	hasAck, err = k.ConsumerLastAckTime.Has(ctx, cid)
	require.NoError(t, err)
	require.False(t, hasAck)
	hasSent, err = k.ConsumerHighestSentVscId.Has(ctx, cid)
	require.NoError(t, err)
	require.False(t, hasSent)
	hasAcked, err = k.ConsumerHighestAckedVscId.Has(ctx, cid)
	require.NoError(t, err)
	require.False(t, hasAcked)

	// getters return their absent-defaults after deletion
	require.Equal(t, ctx.BlockTime(), k.GetConsumerLastAckTime(ctx, cid))
	require.Equal(t, uint64(0), k.GetConsumerHighestSentVscId(ctx, cid))
	require.Equal(t, uint64(0), k.GetConsumerHighestAckedVscId(ctx, cid))
}
