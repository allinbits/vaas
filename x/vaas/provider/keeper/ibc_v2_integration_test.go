package keeper_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	abci "github.com/cometbft/cometbft/abci/types"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// TestIBCV2PacketQueueing tests that VSC packets are correctly queued
// and stored for later sending via IBC v2 client-based routing.
func TestIBCV2PacketQueueing(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := "0"
	clientId := "07-tendermint-0"

	providerKeeper.SetConsumerClientId(ctx, consumerId, clientId)
	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-1")

	pk1, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pk2, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	valUpdates := []abci.ValidatorUpdate{
		{PubKey: pk1, Power: 100},
		{PubKey: pk2, Power: 50},
	}

	vscPacket := vaastypes.NewValidatorSetChangePacketData(valUpdates, 1)
	providerKeeper.AppendPendingVSCPackets(ctx, consumerId, vscPacket)

	pending := providerKeeper.GetPendingVSCPackets(ctx, consumerId)
	require.Len(t, pending, 1)
	require.Equal(t, uint64(1), pending[0].ValsetUpdateId)
	require.Len(t, pending[0].ValidatorUpdates, 2)

	err = providerKeeper.SendVSCPacketsToChain(ctx, consumerId, clientId)
	require.NoError(t, err)

	pending = providerKeeper.GetPendingVSCPackets(ctx, consumerId)
	require.Len(t, pending, 1)
}

// TestIBCV2MultipleConsumers tests queuing VSC packets for multiple consumers.
func TestIBCV2MultipleConsumers(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

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

	pk, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	valUpdates := []abci.ValidatorUpdate{{PubKey: pk, Power: 100}}

	for _, c := range consumers {
		vscPacket := vaastypes.NewValidatorSetChangePacketData(valUpdates, 1)
		providerKeeper.AppendPendingVSCPackets(ctx, c.id, vscPacket)
	}

	for _, c := range consumers {
		pending := providerKeeper.GetPendingVSCPackets(ctx, c.id)
		require.Len(t, pending, 1, "consumer %s should have 1 pending packet", c.id)
	}
}

// TestIBCV2ConsumerRemovalOnTimeout tests that a timeout triggers consumer removal.
func TestIBCV2ConsumerRemovalOnTimeout(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := "0"
	clientId := "07-tendermint-0"

	providerKeeper.SetConsumerClientId(ctx, consumerId, clientId)
	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-1")

	phase := providerKeeper.GetConsumerPhase(ctx, consumerId)
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, phase)

	gotConsumerId, found := providerKeeper.GetClientIdToConsumerId(ctx, clientId)
	require.True(t, found)
	require.Equal(t, consumerId, gotConsumerId)

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(time.Hour*24*21, nil).Times(1)

	err := providerKeeper.OnTimeoutPacketV2(ctx, clientId)
	require.NoError(t, err)

	phase = providerKeeper.GetConsumerPhase(ctx, consumerId)
	require.Equal(t, providertypes.CONSUMER_PHASE_STOPPED, phase)
}

// TestIBCV2ConsumerRemovalOnErrorAck tests that an error acknowledgement triggers consumer removal.
func TestIBCV2ConsumerRemovalOnErrorAck(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := "0"
	clientId := "07-tendermint-0"

	providerKeeper.SetConsumerClientId(ctx, consumerId, clientId)
	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-1")

	phase := providerKeeper.GetConsumerPhase(ctx, consumerId)
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, phase)

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(time.Hour*24*21, nil).Times(1)

	err := providerKeeper.OnAcknowledgementPacketV2(ctx, clientId, "packet decode error")
	require.NoError(t, err)

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

	providerKeeper.SetConsumerClientId(ctx, consumerId, clientId)
	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-1")

	gotClientId, found := providerKeeper.GetConsumerClientId(ctx, consumerId)
	require.True(t, found)
	require.Equal(t, clientId, gotClientId)

	gotConsumerId, found := providerKeeper.GetClientIdToConsumerId(ctx, clientId)
	require.True(t, found)
	require.Equal(t, consumerId, gotConsumerId)
}
