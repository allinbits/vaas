package keeper_test

import (
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// TestOnAcknowledgementPacketV2 tests the IBC v2 acknowledgement handler.
func TestOnAcknowledgementPacketV2(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := "0"
	clientId := "07-tendermint-0"

	// Setup consumer with client mapping
	providerKeeper.SetConsumerClientId(ctx, consumerId, clientId)
	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-chain")

	// Test 1: Success acknowledgement (empty error) - no action needed
	err := providerKeeper.OnAcknowledgementPacketV2(ctx, clientId, "")
	require.NoError(t, err)

	// Consumer should still be launched
	phase := providerKeeper.GetConsumerPhase(ctx, consumerId)
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, phase)

	// Setup mock expectation for StopAndPrepareForConsumerRemoval
	// which calls UnbondingTime
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(time.Hour*24*21, nil).Times(1)

	// Test 2: Error acknowledgement - should trigger consumer removal
	err = providerKeeper.OnAcknowledgementPacketV2(ctx, clientId, "packet data could not be decoded")
	require.NoError(t, err)

	// Consumer should now be stopped
	phase = providerKeeper.GetConsumerPhase(ctx, consumerId)
	require.Equal(t, providertypes.CONSUMER_PHASE_STOPPED, phase)
}

// TestOnAcknowledgementPacketV2UnknownClient tests error handling for unknown clients.
func TestOnAcknowledgementPacketV2UnknownClient(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	unknownClientId := "07-tendermint-999"

	// Error ack with unknown client should return error
	err := providerKeeper.OnAcknowledgementPacketV2(ctx, unknownClientId, "some error")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown client")
}

// TestOnTimeoutPacketV2 tests the IBC v2 timeout handler.
func TestOnTimeoutPacketV2(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := "0"
	clientId := "07-tendermint-0"

	// Setup consumer with client mapping
	providerKeeper.SetConsumerClientId(ctx, consumerId, clientId)
	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-chain")

	// Verify consumer is launched
	phase := providerKeeper.GetConsumerPhase(ctx, consumerId)
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, phase)

	// Setup mock expectation for StopAndPrepareForConsumerRemoval
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(time.Hour*24*21, nil).Times(1)

	// Timeout should trigger consumer removal
	err := providerKeeper.OnTimeoutPacketV2(ctx, clientId)
	require.NoError(t, err)

	// Consumer should now be stopped
	phase = providerKeeper.GetConsumerPhase(ctx, consumerId)
	require.Equal(t, providertypes.CONSUMER_PHASE_STOPPED, phase)
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

	consumerId := "consumer-1"
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

// TestSendVSCPacketsToChainV2NoHandler tests that SendVSCPacketsToChainV2 gracefully
// handles the case when no IBC packet handler is configured.
func TestSendVSCPacketsToChainV2NoHandler(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := "0"
	clientId := "07-tendermint-0"

	// Setup consumer with pending packets
	providerKeeper.SetConsumerClientId(ctx, consumerId, clientId)
	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)

	// Add a pending VSC packet
	providerKeeper.AppendPendingVSCPackets(ctx, consumerId, vaastypes.ValidatorSetChangePacketData{
		ValidatorUpdates: []abci.ValidatorUpdate{},
		ValsetUpdateId:   1,
	})

	// Without setting IBCPacketHandler, SendVSCPacketsToChainV2 should return nil
	// and not send any packets (graceful no-op)
	err := providerKeeper.SendVSCPacketsToChainV2(ctx, consumerId, clientId)
	require.NoError(t, err)

	// Pending packets should still be there since no handler was configured
	pending := providerKeeper.GetPendingVSCPackets(ctx, consumerId)
	require.Len(t, pending, 1)
}

// TestSendVSCPacketsToChainV2WithHandler tests SendVSCPacketsToChainV2 with a mock handler.
func TestSendVSCPacketsToChainV2WithHandler(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := "0"
	clientId := "07-tendermint-0"

	// Setup consumer
	providerKeeper.SetConsumerClientId(ctx, consumerId, clientId)
	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-chain")

	// Set VAAS timeout period in params
	params := providertypes.DefaultParams()
	providerKeeper.SetParams(ctx, params)

	// Add pending VSC packets
	providerKeeper.AppendPendingVSCPackets(ctx, consumerId, vaastypes.ValidatorSetChangePacketData{
		ValidatorUpdates: []abci.ValidatorUpdate{},
		ValsetUpdateId:   1,
	})
	providerKeeper.AppendPendingVSCPackets(ctx, consumerId, vaastypes.ValidatorSetChangePacketData{
		ValidatorUpdates: []abci.ValidatorUpdate{},
		ValsetUpdateId:   2,
	})

	// Create and set a mock handler
	mockHandler := testkeeper.NewMockIBCPacketHandler(ctrl)
	providerKeeper.SetIBCPacketHandler(mockHandler)

	// Expect SendPacket to be called twice (once for each pending packet)
	mockHandler.EXPECT().SendPacket(
		gomock.Any(), // ctx
		clientId,     // sourceClient
		vaastypes.ConsumerAppID, // destApp
		gomock.Any(), // timeoutTimestamp
		gomock.Any(), // data
	).Return(uint64(1), nil).Times(1)

	mockHandler.EXPECT().SendPacket(
		gomock.Any(),
		clientId,
		vaastypes.ConsumerAppID,
		gomock.Any(),
		gomock.Any(),
	).Return(uint64(2), nil).Times(1)

	// Send packets
	err := providerKeeper.SendVSCPacketsToChainV2(ctx, consumerId, clientId)
	require.NoError(t, err)

	// Pending packets should be deleted after successful send
	pending := providerKeeper.GetPendingVSCPackets(ctx, consumerId)
	require.Len(t, pending, 0)
}
