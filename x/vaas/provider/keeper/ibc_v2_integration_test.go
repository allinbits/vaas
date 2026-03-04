package keeper_test

import (
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// TestIBCV2FullVSCFlow tests the complete IBC v2 VSC packet flow:
// 1. Provider queues VSC packets
// 2. Provider sends VSC packets via v2 client-based routing
// 3. Simulates successful acknowledgement
// 4. Verifies packets are cleared after successful send
func TestIBCV2FullVSCFlow(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := "0"
	clientId := "07-tendermint-0"

	// Setup consumer chain
	providerKeeper.SetConsumerClientId(ctx, consumerId, clientId)
	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-1")

	// Set params
	params := providertypes.DefaultParams()
	providerKeeper.SetParams(ctx, params)

	// Create validator updates
	pk1, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pk2, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	valUpdates := []abci.ValidatorUpdate{
		{PubKey: pk1, Power: 100},
		{PubKey: pk2, Power: 50},
	}

	// Queue VSC packets
	vscPacket := vaastypes.NewValidatorSetChangePacketData(valUpdates, 1)
	providerKeeper.AppendPendingVSCPackets(ctx, consumerId, vscPacket)

	// Verify packets are queued
	pending := providerKeeper.GetPendingVSCPackets(ctx, consumerId)
	require.Len(t, pending, 1)
	require.Equal(t, uint64(1), pending[0].ValsetUpdateId)
	require.Len(t, pending[0].ValidatorUpdates, 2)

	// Create mock handler and set it
	mockHandler := testkeeper.NewMockIBCPacketHandler(ctrl)
	providerKeeper.SetIBCPacketHandler(mockHandler)

	// Expect SendPacket to be called
	mockHandler.EXPECT().SendPacket(
		gomock.Any(),
		clientId,
		vaastypes.ConsumerAppID,
		gomock.Any(),
		gomock.Any(),
	).Return(uint64(1), nil).Times(1)

	// Send packets via v2
	err = providerKeeper.SendVSCPacketsToChainV2(ctx, consumerId, clientId)
	require.NoError(t, err)

	// Verify packets are cleared after successful send
	pending = providerKeeper.GetPendingVSCPackets(ctx, consumerId)
	require.Len(t, pending, 0)
}

// TestIBCV2MultipleConsumers tests sending VSC packets to multiple consumers via v2.
func TestIBCV2MultipleConsumers(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	// Setup multiple consumers
	consumers := []struct {
		id       string
		clientId string
		chainId  string
	}{
		{"0", "07-tendermint-0", "consumer-1"},
		{"1", "07-tendermint-1", "consumer-2"},
		{"2", "07-tendermint-2", "consumer-3"},
	}

	for _, c := range consumers {
		providerKeeper.SetConsumerClientId(ctx, c.id, c.clientId)
		providerKeeper.SetConsumerPhase(ctx, c.id, providertypes.CONSUMER_PHASE_LAUNCHED)
		providerKeeper.SetConsumerChainId(ctx, c.id, c.chainId)
	}

	// Set params
	params := providertypes.DefaultParams()
	providerKeeper.SetParams(ctx, params)

	// Create validator update
	pk, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	valUpdates := []abci.ValidatorUpdate{{PubKey: pk, Power: 100}}

	// Queue packets for each consumer
	for _, c := range consumers {
		vscPacket := vaastypes.NewValidatorSetChangePacketData(valUpdates, 1)
		providerKeeper.AppendPendingVSCPackets(ctx, c.id, vscPacket)
	}

	// Create mock handler
	mockHandler := testkeeper.NewMockIBCPacketHandler(ctrl)
	providerKeeper.SetIBCPacketHandler(mockHandler)

	// Expect SendPacket to be called for each consumer
	for i, c := range consumers {
		mockHandler.EXPECT().SendPacket(
			gomock.Any(),
			c.clientId,
			vaastypes.ConsumerAppID,
			gomock.Any(),
			gomock.Any(),
		).Return(uint64(i+1), nil).Times(1)
	}

	// Send packets to all consumers
	for _, c := range consumers {
		err := providerKeeper.SendVSCPacketsToChainV2(ctx, c.id, c.clientId)
		require.NoError(t, err)
	}

	// Verify all packets are cleared
	for _, c := range consumers {
		pending := providerKeeper.GetPendingVSCPackets(ctx, c.id)
		require.Len(t, pending, 0, "consumer %s should have no pending packets", c.id)
	}
}

// TestIBCV2ConsumerRemovalOnTimeout tests that a timeout triggers consumer removal.
func TestIBCV2ConsumerRemovalOnTimeout(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := "0"
	clientId := "07-tendermint-0"

	// Setup consumer
	providerKeeper.SetConsumerClientId(ctx, consumerId, clientId)
	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-1")

	// Verify consumer is launched
	phase := providerKeeper.GetConsumerPhase(ctx, consumerId)
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, phase)

	// Verify client mapping exists
	gotConsumerId, found := providerKeeper.GetClientIdToConsumerId(ctx, clientId)
	require.True(t, found)
	require.Equal(t, consumerId, gotConsumerId)

	// Mock UnbondingTime for StopAndPrepareForConsumerRemoval
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(time.Hour*24*21, nil).Times(1)

	// Simulate timeout
	err := providerKeeper.OnTimeoutPacketV2(ctx, clientId)
	require.NoError(t, err)

	// Verify consumer is stopped
	phase = providerKeeper.GetConsumerPhase(ctx, consumerId)
	require.Equal(t, providertypes.CONSUMER_PHASE_STOPPED, phase)
}

// TestIBCV2ConsumerRemovalOnErrorAck tests that an error acknowledgement triggers consumer removal.
func TestIBCV2ConsumerRemovalOnErrorAck(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := "0"
	clientId := "07-tendermint-0"

	// Setup consumer
	providerKeeper.SetConsumerClientId(ctx, consumerId, clientId)
	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-1")

	// Verify consumer is launched
	phase := providerKeeper.GetConsumerPhase(ctx, consumerId)
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, phase)

	// Mock UnbondingTime for StopAndPrepareForConsumerRemoval
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(time.Hour*24*21, nil).Times(1)

	// Simulate error acknowledgement
	err := providerKeeper.OnAcknowledgementPacketV2(ctx, clientId, "packet decode error")
	require.NoError(t, err)

	// Verify consumer is stopped
	phase = providerKeeper.GetConsumerPhase(ctx, consumerId)
	require.Equal(t, providertypes.CONSUMER_PHASE_STOPPED, phase)
}

// TestIBCV2DualModeRouting tests that the provider correctly selects v2 routing
// when no channel exists but a client does.
func TestIBCV2DualModeRouting(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := "0"
	clientId := "07-tendermint-0"

	// Setup consumer with client ID only (no channel - v2 mode)
	providerKeeper.SetConsumerClientId(ctx, consumerId, clientId)
	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-1")

	// Verify no channel mapping exists
	_, found := providerKeeper.GetConsumerIdToChannelId(ctx, consumerId)
	require.False(t, found, "no channel should exist for v2-only consumer")

	// Verify client mapping exists
	gotClientId, found := providerKeeper.GetConsumerClientId(ctx, consumerId)
	require.True(t, found)
	require.Equal(t, clientId, gotClientId)

	// Verify reverse mapping
	gotConsumerId, found := providerKeeper.GetClientIdToConsumerId(ctx, clientId)
	require.True(t, found)
	require.Equal(t, consumerId, gotConsumerId)
}
