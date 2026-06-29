package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	keeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
)

func TestFullValSetUpdatesReturnsCompleteSet(t *testing.T) {
	pk1 := ed25519.GenPrivKey().PubKey()
	tmPk1, err := cryptocodec.ToCmtProtoPublicKey(pk1)
	require.NoError(t, err)
	vals := []providertypes.ConsensusValidator{{PublicKey: &tmPk1, Power: 10}}

	updates := keeper.FullValSetUpdates(vals)
	require.Len(t, updates, 1)
	require.Equal(t, int64(10), updates[0].Power)
}

func TestQueueVSCPacketsSnapshotWhenBehind(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerHighestSentVscId(ctx, cid, 5)
	k.SetConsumerHighestAckedVscId(ctx, cid, 3) // behind: acked < sent: acked < sent

	mocks.MockStakingKeeper.EXPECT().MaxValidators(gomock.Any()).Return(uint32(100), nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().GetBondedValidatorsByPower(gomock.Any()).Return([]stakingtypes.Validator{}, nil).AnyTimes()

	require.NoError(t, k.QueueVSCPackets(ctx))
	pending := k.GetPendingVSCPackets(ctx, cid)
	require.Len(t, pending, 1)
	require.True(t, pending[0].IsSnapshot)
}

func TestQueueVSCPacketsDiffWhenCaughtUp(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
	// acked == sent == 0: caught up
	k.SetConsumerHighestSentVscId(ctx, cid, 0)
	k.SetConsumerHighestAckedVscId(ctx, cid, 0)

	mocks.MockStakingKeeper.EXPECT().MaxValidators(gomock.Any()).Return(uint32(100), nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().GetBondedValidatorsByPower(gomock.Any()).Return([]stakingtypes.Validator{}, nil).AnyTimes()

	require.NoError(t, k.QueueVSCPackets(ctx))
	pending := k.GetPendingVSCPackets(ctx, cid)
	require.Len(t, pending, 1)
	require.False(t, pending[0].IsSnapshot)
}
