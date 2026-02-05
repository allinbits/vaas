package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	"github.com/allinbits/vaas/x/vaas/types"
)

// TestIBCV2ConsumerFullVSCFlow tests the complete consumer-side IBC v2 VSC packet flow:
// 1. Consumer receives first VSC packet and establishes provider client
// 2. Consumer accumulates validator updates
// 3. Consumer tracks highest valset update ID for out-of-order handling
func TestIBCV2ConsumerFullVSCFlow(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	providerClientID := "07-tendermint-0"

	// Create validator updates
	pk1, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pk2, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	// Step 1: Receive first VSC packet - should establish provider client
	valUpdates1 := []abci.ValidatorUpdate{
		{PubKey: pk1, Power: 100},
	}
	vscPacket1 := types.NewValidatorSetChangePacketData(valUpdates1, 1)

	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, vscPacket1)
	require.NoError(t, err)

	// Verify provider client was established
	clientID, found := consumerKeeper.GetProviderClientID(ctx)
	require.True(t, found)
	require.Equal(t, providerClientID, clientID)

	// Verify pending changes
	pendingChanges, found := consumerKeeper.GetPendingChanges(ctx)
	require.True(t, found)
	require.Len(t, pendingChanges.ValidatorUpdates, 1)
	require.Equal(t, int64(100), pendingChanges.ValidatorUpdates[0].Power)

	// Verify highest valset update ID
	highestID := consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.Equal(t, uint64(1), highestID)

	// Step 2: Receive second VSC packet - should accumulate updates
	valUpdates2 := []abci.ValidatorUpdate{
		{PubKey: pk2, Power: 50},
	}
	vscPacket2 := types.NewValidatorSetChangePacketData(valUpdates2, 2)

	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, vscPacket2)
	require.NoError(t, err)

	// Verify accumulated pending changes
	pendingChanges, found = consumerKeeper.GetPendingChanges(ctx)
	require.True(t, found)
	require.Len(t, pendingChanges.ValidatorUpdates, 2)

	// Verify highest valset update ID updated
	highestID = consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.Equal(t, uint64(2), highestID)
}

// TestIBCV2ConsumerOutOfOrderHandling tests that out-of-order packets are
// acknowledged but not processed.
func TestIBCV2ConsumerOutOfOrderHandling(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	providerClientID := "07-tendermint-0"

	pk1, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pk2, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	// Send packet with valset ID 5 first (simulating out-of-order delivery)
	valUpdates5 := []abci.ValidatorUpdate{{PubKey: pk1, Power: 500}}
	vscPacket5 := types.NewValidatorSetChangePacketData(valUpdates5, 5)

	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, vscPacket5)
	require.NoError(t, err)

	highestID := consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.Equal(t, uint64(5), highestID)

	// Send packet with valset ID 3 (arrives late - should be ignored)
	valUpdates3 := []abci.ValidatorUpdate{{PubKey: pk2, Power: 300}}
	vscPacket3 := types.NewValidatorSetChangePacketData(valUpdates3, 3)

	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, vscPacket3)
	require.NoError(t, err, "out-of-order packet should be acknowledged without error")

	// Highest ID should still be 5
	highestID = consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.Equal(t, uint64(5), highestID)

	// Pending changes should only contain pk1 (from packet 5)
	pendingChanges, _ := consumerKeeper.GetPendingChanges(ctx)
	require.Len(t, pendingChanges.ValidatorUpdates, 1)
	require.Equal(t, int64(500), pendingChanges.ValidatorUpdates[0].Power)

	// Send packet with valset ID 6 (should be processed normally)
	valUpdates6 := []abci.ValidatorUpdate{{PubKey: pk2, Power: 600}}
	vscPacket6 := types.NewValidatorSetChangePacketData(valUpdates6, 6)

	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, vscPacket6)
	require.NoError(t, err)

	highestID = consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.Equal(t, uint64(6), highestID)

	// Pending changes should now include pk2
	pendingChanges, _ = consumerKeeper.GetPendingChanges(ctx)
	require.Len(t, pendingChanges.ValidatorUpdates, 2)
}

// TestIBCV2ConsumerRejectsUnknownProvider tests that packets from an unknown
// provider client are rejected after the provider is established.
func TestIBCV2ConsumerRejectsUnknownProvider(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	providerClientID := "07-tendermint-0"
	unknownClientID := "07-tendermint-999"

	pk, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	// Establish provider with first packet
	valUpdates := []abci.ValidatorUpdate{{PubKey: pk, Power: 100}}
	vscPacket := types.NewValidatorSetChangePacketData(valUpdates, 1)

	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, vscPacket)
	require.NoError(t, err)

	// Verify provider is established
	clientID, found := consumerKeeper.GetProviderClientID(ctx)
	require.True(t, found)
	require.Equal(t, providerClientID, clientID)

	// Try to send packet from unknown client - should fail
	vscPacket2 := types.NewValidatorSetChangePacketData(valUpdates, 2)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, unknownClientID, vscPacket2)
	require.Error(t, err, "packet from unknown client should be rejected")
	require.Contains(t, err.Error(), "unexpected client")

	// Highest ID should still be 1 (packet 2 was rejected)
	highestID := consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.Equal(t, uint64(1), highestID)
}

// TestIBCV2ConsumerProviderInfoQuery tests the v2 provider info query.
func TestIBCV2ConsumerProviderInfoQuery(t *testing.T) {
	consumerKeeper, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	providerClientID := "07-tendermint-0"

	// Initially, no provider client - v2 query should fail
	_, err := consumerKeeper.GetProviderInfoV2(ctx)
	require.Error(t, err)

	// Set provider client ID
	consumerKeeper.SetProviderClientID(ctx, providerClientID)

	// Mock GetClientState for the query
	mocks.MockClientKeeper.EXPECT().GetClientState(ctx, providerClientID).Return(nil, false).Times(1)

	// Query should fail because client state not found
	_, err = consumerKeeper.GetProviderInfoV2(ctx)
	require.Error(t, err)
}

// TestIBCV2ConsumerDuplicatePacketHandling tests that duplicate packets
// (same valset update ID) are acknowledged but ignored.
func TestIBCV2ConsumerDuplicatePacketHandling(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	providerClientID := "07-tendermint-0"

	pk, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	// Send first packet
	valUpdates := []abci.ValidatorUpdate{{PubKey: pk, Power: 100}}
	vscPacket := types.NewValidatorSetChangePacketData(valUpdates, 5)

	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, vscPacket)
	require.NoError(t, err)

	pendingChanges, _ := consumerKeeper.GetPendingChanges(ctx)
	require.Len(t, pendingChanges.ValidatorUpdates, 1)
	require.Equal(t, int64(100), pendingChanges.ValidatorUpdates[0].Power)

	// Send duplicate packet (same valset update ID, different power)
	valUpdatesDup := []abci.ValidatorUpdate{{PubKey: pk, Power: 999}}
	vscPacketDup := types.NewValidatorSetChangePacketData(valUpdatesDup, 5)

	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, vscPacketDup)
	require.NoError(t, err, "duplicate packet should be acknowledged")

	// Pending changes should still have original power (duplicate ignored)
	pendingChanges, _ = consumerKeeper.GetPendingChanges(ctx)
	require.Len(t, pendingChanges.ValidatorUpdates, 1)
	require.Equal(t, int64(100), pendingChanges.ValidatorUpdates[0].Power)
}
