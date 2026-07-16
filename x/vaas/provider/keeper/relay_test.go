package keeper_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	abci "github.com/cometbft/cometbft/abci/types"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"

	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// TestOnAcknowledgementPacketV2 tests the IBC v2 acknowledgement handler.
func TestOnAcknowledgementPacketV2(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	clientId := "07-tendermint-0"

	// Setup consumer with client mapping
	providerKeeper.SetConsumerClientId(ctx, consumerId, clientId)
	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-chain")

	// Test 1: Success acknowledgement (empty error) - no action needed
	err := providerKeeper.OnAcknowledgementPacketV2(ctx, clientId, 1, "")
	require.NoError(t, err)

	// Consumer should still be launched
	phase := providerKeeper.GetConsumerPhase(ctx, consumerId)
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, phase)

	// Test 2: Error acknowledgement - liveness sweep owns removal, consumer stays launched
	err = providerKeeper.OnAcknowledgementPacketV2(ctx, clientId, 1, "packet data could not be decoded")
	require.NoError(t, err)

	// Consumer stays launched; liveness sweep will handle removal
	phase = providerKeeper.GetConsumerPhase(ctx, consumerId)
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, phase)
}

// TestOnAcknowledgementPacketV2UnknownClient tests error handling for unknown clients.
func TestOnAcknowledgementPacketV2UnknownClient(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	unknownClientId := "07-tendermint-999"

	// Error ack with unknown client should return error
	err := providerKeeper.OnAcknowledgementPacketV2(ctx, unknownClientId, 0, "some error")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown client")
}

// TestOnTimeoutPacketV2 tests the IBC v2 timeout handler.
func TestOnTimeoutPacketV2(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	clientId := "07-tendermint-0"

	// Setup consumer with client mapping
	providerKeeper.SetConsumerClientId(ctx, consumerId, clientId)
	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-chain")

	// Verify consumer is launched
	phase := providerKeeper.GetConsumerPhase(ctx, consumerId)
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, phase)

	// Timeout is log-only; liveness sweep owns removal
	err := providerKeeper.OnTimeoutPacketV2(ctx, clientId)
	require.NoError(t, err)

	// Consumer stays launched; liveness sweep will handle removal
	phase = providerKeeper.GetConsumerPhase(ctx, consumerId)
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, phase)
}

// TestOnTimeoutPacketV2UnknownClient tests error handling for unknown clients.
func TestOnTimeoutPacketV2UnknownClient(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	unknownClientId := "07-tendermint-999"

	// Timeout with unknown client should return error
	err := providerKeeper.OnTimeoutPacketV2(ctx, unknownClientId)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown client")
}

// TestClientIdToConsumerIdMapping tests the client ID to consumer ID mapping used in IBC v2.
func TestClientIdToConsumerIdMapping(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(1)
	clientId := "07-tendermint-0"

	// Initially no mapping
	_, found := providerKeeper.GetClientIdToConsumerId(ctx, clientId)
	require.False(t, found)

	// Set the mapping via SetConsumerClientId (which sets both directions)
	providerKeeper.SetConsumerClientId(ctx, consumerId, clientId)

	// Verify forward mapping
	gotClientId, found := providerKeeper.GetConsumerClientId(ctx, consumerId)
	require.True(t, found)
	require.Equal(t, clientId, gotClientId)

	// Verify reverse mapping
	gotConsumerId, found := providerKeeper.GetClientIdToConsumerId(ctx, clientId)
	require.True(t, found)
	require.Equal(t, consumerId, gotConsumerId)

	// Delete and verify both mappings are removed
	providerKeeper.DeleteConsumerClientId(ctx, consumerId)

	_, found = providerKeeper.GetConsumerClientId(ctx, consumerId)
	require.False(t, found)
	_, found = providerKeeper.GetClientIdToConsumerId(ctx, clientId)
	require.False(t, found)
}

// TestSendVSCPacketsToChainNoHandler tests that SendVSCPacketsToChain gracefully
// handles the case when the IBC v2 channel keeper returns ErrClientNotActive.
func TestSendVSCPacketsToChainNoHandler(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	clientId := "07-tendermint-0"

	// Setup consumer with pending packets
	providerKeeper.SetConsumerClientId(ctx, consumerId, clientId)
	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)

	// Add a pending VSC packet
	providerKeeper.AppendPendingVSCPackets(ctx, consumerId, vaastypes.ValidatorSetChangePacketData{
		ValidatorUpdates: []abci.ValidatorUpdate{},
		ValsetUpdateId:   1,
	})

	// Simulate an inactive client so packets are not sent
	mocks.MockChannelV2Keeper.EXPECT().
		SendPacket(gomock.Any(), gomock.Any()).
		Return(nil, clienttypes.ErrClientNotActive)

	err := providerKeeper.SendVSCPacketsToChain(ctx, consumerId, clientId)
	require.NoError(t, err)

	// Pending packets should still be there since the client was not active
	pending := providerKeeper.GetPendingVSCPackets(ctx, consumerId)
	require.Len(t, pending, 1)
}

// TestOnAckHighestAckedDoesNotRegress asserts that a lower vscId arriving after
// a higher one does not regress the highestAcked counter.
func TestOnAckHighestAckedDoesNotRegress(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	clientId := "07-tendermint-0"
	k.SetConsumerClientId(ctx, consumerId, clientId)
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)

	// First ack: vscId 9.
	require.NoError(t, k.OnAcknowledgementPacketV2(ctx, clientId, 9, ""))
	require.Equal(t, uint64(9), k.GetConsumerHighestAckedVscId(ctx, consumerId))

	// Out-of-order ack: vscId 5 must not regress the counter.
	require.NoError(t, k.OnAcknowledgementPacketV2(ctx, clientId, 5, ""))
	require.Equal(t, uint64(9), k.GetConsumerHighestAckedVscId(ctx, consumerId))
}

// TestHighestSentAdvancesOnlyOnSuccess verifies that highestSent and pending
// packets are unchanged when SendPacket returns a non-ErrClientNotActive error,
// and that they are updated correctly on success.
func TestHighestSentAdvancesOnlyOnSuccess(t *testing.T) {
	consumerId := uint64(0)
	clientId := "07-tendermint-0"

	t.Run("send error leaves state unchanged", func(t *testing.T) {
		k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
		defer ctrl.Finish()

		k.SetConsumerClientId(ctx, consumerId, clientId)
		k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)

		k.AppendPendingVSCPackets(ctx, consumerId, vaastypes.ValidatorSetChangePacketData{
			ValidatorUpdates: []abci.ValidatorUpdate{},
			ValsetUpdateId:   3,
		})
		k.AppendPendingVSCPackets(ctx, consumerId, vaastypes.ValidatorSetChangePacketData{
			ValidatorUpdates: []abci.ValidatorUpdate{},
			ValsetUpdateId:   5,
		})

		// Non-ErrClientNotActive error: function logs and returns nil, leaving pending intact.
		mocks.MockChannelV2Keeper.EXPECT().
			SendPacket(gomock.Any(), gomock.Any()).
			Return(nil, fmt.Errorf("some transient send error"))

		require.NoError(t, k.SendVSCPacketsToChain(ctx, consumerId, clientId))

		require.Equal(t, uint64(0), k.GetConsumerHighestSentVscId(ctx, consumerId))
		require.Len(t, k.GetPendingVSCPackets(ctx, consumerId), 2)
	})

	t.Run("send success advances highestSent and clears pending", func(t *testing.T) {
		k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
		defer ctrl.Finish()

		k.SetConsumerClientId(ctx, consumerId, clientId)
		k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)

		k.AppendPendingVSCPackets(ctx, consumerId, vaastypes.ValidatorSetChangePacketData{
			ValidatorUpdates: []abci.ValidatorUpdate{},
			ValsetUpdateId:   3,
		})
		k.AppendPendingVSCPackets(ctx, consumerId, vaastypes.ValidatorSetChangePacketData{
			ValidatorUpdates: []abci.ValidatorUpdate{},
			ValsetUpdateId:   5,
		})

		// Both packets sent successfully.
		mocks.MockChannelV2Keeper.EXPECT().
			SendPacket(gomock.Any(), gomock.Any()).
			Return(&channeltypesv2.MsgSendPacketResponse{Sequence: 1}, nil).Times(1)
		mocks.MockChannelV2Keeper.EXPECT().
			SendPacket(gomock.Any(), gomock.Any()).
			Return(&channeltypesv2.MsgSendPacketResponse{Sequence: 2}, nil).Times(1)

		require.NoError(t, k.SendVSCPacketsToChain(ctx, consumerId, clientId))

		require.Equal(t, uint64(5), k.GetConsumerHighestSentVscId(ctx, consumerId))
		require.Empty(t, k.GetPendingVSCPackets(ctx, consumerId))
	})

	t.Run("partial send: first succeeds, second fails, advances to the sent id and leaves pending", func(t *testing.T) {
		k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
		defer ctrl.Finish()

		k.SetConsumerClientId(ctx, consumerId, clientId)
		k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)

		k.AppendPendingVSCPackets(ctx, consumerId, vaastypes.ValidatorSetChangePacketData{
			ValidatorUpdates: []abci.ValidatorUpdate{},
			ValsetUpdateId:   3,
		})
		k.AppendPendingVSCPackets(ctx, consumerId, vaastypes.ValidatorSetChangePacketData{
			ValidatorUpdates: []abci.ValidatorUpdate{},
			ValsetUpdateId:   5,
		})

		// First packet (vscId 3) sends and advances highestSent; the second
		// (vscId 5) fails, so SendVSCPacketsToChain returns nil early without
		// reaching DeletePendingVSCPackets -- all pending packets stay queued.
		mocks.MockChannelV2Keeper.EXPECT().
			SendPacket(gomock.Any(), gomock.Any()).
			Return(&channeltypesv2.MsgSendPacketResponse{Sequence: 1}, nil).Times(1)
		mocks.MockChannelV2Keeper.EXPECT().
			SendPacket(gomock.Any(), gomock.Any()).
			Return(nil, fmt.Errorf("some transient send error")).Times(1)

		require.NoError(t, k.SendVSCPacketsToChain(ctx, consumerId, clientId))

		require.Equal(t, uint64(3), k.GetConsumerHighestSentVscId(ctx, consumerId))
		require.Len(t, k.GetPendingVSCPackets(ctx, consumerId), 2)
	})
}

// TestQueueVSCPacketsStampsDowntimeParams verifies that every packet queued by
// QueueVSCPackets carries the provider's current downtime detection params, so
// consumers stay in sync with the provider without a dedicated update message.
func TestQueueVSCPacketsStampsDowntimeParams(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	k.SetInfractionParams(ctx, providertypes.DefaultInfractionParameters())

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)

	mocks.MockStakingKeeper.EXPECT().MaxValidators(gomock.Any()).Return(uint32(100), nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().GetBondedValidatorsByPower(gomock.Any()).Return([]stakingtypes.Validator{}, nil).AnyTimes()

	require.NoError(t, k.QueueVSCPackets(ctx))

	pending := k.GetPendingVSCPackets(ctx, cid)
	require.Len(t, pending, 1)
	require.NotNil(t, pending[0].DowntimeParams)
	require.Equal(t, int64(600), pending[0].DowntimeParams.SignedBlocksWindow)
}
