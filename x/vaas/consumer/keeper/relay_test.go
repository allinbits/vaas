package keeper_test

import (
	"sort"
	"testing"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	channeltypes "github.com/cosmos/ibc-go/v10/modules/core/04-channel/types"
	"github.com/stretchr/testify/require"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"

	abci "github.com/cometbft/cometbft/abci/types"

	"github.com/allinbits/vaas/testutil/crypto"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	"github.com/allinbits/vaas/x/vaas/types"
)

// TestOnRecvVSCPacket tests the behavior of OnRecvVSCPacket over various packet scenarios
func TestOnRecvVSCPacket(t *testing.T) {
	consumerCCVChannelID := "consumerCCVChannelID"
	providerCCVChannelID := "providerCCVChannelID"

	pk1, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pk2, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pk3, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	changes1 := []abci.ValidatorUpdate{
		{
			PubKey: pk1,
			Power:  30,
		},
		{
			PubKey: pk2,
			Power:  20,
		},
	}

	changes2 := []abci.ValidatorUpdate{
		{
			PubKey: pk2,
			Power:  40,
		},
		{
			PubKey: pk3,
			Power:  10,
		},
	}

	pd := types.NewValidatorSetChangePacketData(
		changes1,
		1,
	)

	pd2 := types.NewValidatorSetChangePacketData(
		changes2,
		2,
	)

	testCases := []struct {
		name                   string
		expError               bool
		packet                 channeltypes.Packet
		expectedPendingChanges types.ValidatorSetChangePacketData
	}{
		{
			"success on first packet",
			false,
			channeltypes.NewPacket(pd.GetBytes(), 1, types.ProviderPortID, providerCCVChannelID, types.ConsumerPortID, consumerCCVChannelID,
				clienttypes.NewHeight(1, 0), 0),
			types.ValidatorSetChangePacketData{ValidatorUpdates: changes1},
		},
		{
			"success on subsequent packet",
			false,
			channeltypes.NewPacket(pd.GetBytes(), 2, types.ProviderPortID, providerCCVChannelID, types.ConsumerPortID, consumerCCVChannelID,
				clienttypes.NewHeight(1, 0), 0),
			types.ValidatorSetChangePacketData{ValidatorUpdates: changes1},
		},
		{
			"success on packet with more changes",
			false,
			channeltypes.NewPacket(pd2.GetBytes(), 3, types.ProviderPortID, providerCCVChannelID, types.ConsumerPortID, consumerCCVChannelID,
				clienttypes.NewHeight(1, 0), 0),
			types.ValidatorSetChangePacketData{ValidatorUpdates: []abci.ValidatorUpdate{
				{
					PubKey: pk1,
					Power:  30,
				},
				{
					PubKey: pk2,
					Power:  40,
				},
				{
					PubKey: pk3,
					Power:  10,
				},
			}},
		},
	}

	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	// Set channel to provider, still in context of consumer chain
	consumerKeeper.SetProviderChannel(ctx, consumerCCVChannelID)

	for _, tc := range testCases {
		var newChanges types.ValidatorSetChangePacketData
		err := types.ModuleCdc.UnmarshalJSON(tc.packet.GetData(), &newChanges)
		require.Nil(t, err, "invalid test case: %s - cannot unmarshal VSCPacket data", tc.name)
		err = consumerKeeper.OnRecvVSCPacket(ctx, tc.packet, newChanges)
		if tc.expError {
			require.Error(t, err, "%s - invalid but OnRecvVSCPacket did not return error", tc.name)
			continue
		}
		require.NoError(t, err, "%s - valid but OnRecvVSCPacket returned error: %w", tc.name, err)

		providerChannel, ok := consumerKeeper.GetProviderChannel(ctx)
		require.True(t, ok)
		require.Equal(t, tc.packet.DestinationChannel, providerChannel,
			"provider channel is not destination channel on successful receive for valid test case: %s", tc.name)

		// Check that pending changes are accumulated and stored correctly
		actualPendingChanges, ok := consumerKeeper.GetPendingChanges(ctx)
		require.True(t, ok)
		// Sort to avoid dumb inequalities
		sort.SliceStable(actualPendingChanges.ValidatorUpdates, func(i, j int) bool {
			return actualPendingChanges.ValidatorUpdates[i].PubKey.Compare(actualPendingChanges.ValidatorUpdates[j].PubKey) == -1
		})
		sort.SliceStable(tc.expectedPendingChanges.ValidatorUpdates, func(i, j int) bool {
			return tc.expectedPendingChanges.ValidatorUpdates[i].PubKey.Compare(tc.expectedPendingChanges.ValidatorUpdates[j].PubKey) == -1
		})
		require.Equal(t, tc.expectedPendingChanges, *actualPendingChanges, "pending changes not equal to expected changes after successful packet receive. case: %s", tc.name)
	}
}

// TestOnRecvVSCPacketV2 tests the behavior of OnRecvVSCPacketV2 (IBC v2 client-based routing)
// over various packet scenarios including out-of-order packet handling.
func TestOnRecvVSCPacketV2(t *testing.T) {
	providerClientID := "07-tendermint-0"

	pk1, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pk2, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pk3, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	changes1 := []abci.ValidatorUpdate{
		{PubKey: pk1, Power: 30},
		{PubKey: pk2, Power: 20},
	}

	changes2 := []abci.ValidatorUpdate{
		{PubKey: pk2, Power: 40},
		{PubKey: pk3, Power: 10},
	}

	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	// Test 1: First packet establishes provider client
	pd1 := types.NewValidatorSetChangePacketData(changes1, 1)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd1)
	require.NoError(t, err, "first packet should succeed")

	// Verify provider client was set
	clientID, found := consumerKeeper.GetProviderClientID(ctx)
	require.True(t, found)
	require.Equal(t, providerClientID, clientID)

	// Verify pending changes
	pendingChanges, ok := consumerKeeper.GetPendingChanges(ctx)
	require.True(t, ok)
	require.Equal(t, 2, len(pendingChanges.ValidatorUpdates))

	// Verify highest valset update ID
	highestID := consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.Equal(t, uint64(1), highestID)

	// Test 2: Second packet with higher valset ID
	pd2 := types.NewValidatorSetChangePacketData(changes2, 2)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd2)
	require.NoError(t, err, "second packet should succeed")

	highestID = consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.Equal(t, uint64(2), highestID)

	// Test 3: Packet from different client should fail
	differentClientID := "07-tendermint-999"
	pd3 := types.NewValidatorSetChangePacketData(changes1, 3)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, differentClientID, pd3)
	require.Error(t, err, "packet from different client should fail")

	// Highest ID should not have changed
	highestID = consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.Equal(t, uint64(2), highestID)
}

// TestOnRecvVSCPacketV2OutOfOrder tests out-of-order packet handling in IBC v2.
// Packets with valset_update_id <= highest processed ID should be acknowledged but ignored.
func TestOnRecvVSCPacketV2OutOfOrder(t *testing.T) {
	providerClientID := "07-tendermint-0"

	pk1, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pk2, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	// Send packet with valset ID 5 first
	changes5 := []abci.ValidatorUpdate{{PubKey: pk1, Power: 50}}
	pd5 := types.NewValidatorSetChangePacketData(changes5, 5)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd5)
	require.NoError(t, err)

	highestID := consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.Equal(t, uint64(5), highestID)

	pendingChanges, _ := consumerKeeper.GetPendingChanges(ctx)
	require.Equal(t, int64(50), pendingChanges.ValidatorUpdates[0].Power)

	// Send packet with valset ID 3 (out of order - should be ignored)
	changes3 := []abci.ValidatorUpdate{{PubKey: pk2, Power: 30}}
	pd3 := types.NewValidatorSetChangePacketData(changes3, 3)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd3)
	require.NoError(t, err, "out-of-order packet should be acknowledged without error")

	// Highest ID should still be 5
	highestID = consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.Equal(t, uint64(5), highestID)

	// Pending changes should still only contain pk1 with power 50
	pendingChanges, _ = consumerKeeper.GetPendingChanges(ctx)
	require.Equal(t, 1, len(pendingChanges.ValidatorUpdates))
	require.Equal(t, int64(50), pendingChanges.ValidatorUpdates[0].Power)

	// Send packet with valset ID 5 again (duplicate - should be ignored)
	pd5Dup := types.NewValidatorSetChangePacketData(changes3, 5)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd5Dup)
	require.NoError(t, err, "duplicate packet should be acknowledged without error")

	// Highest ID should still be 5
	highestID = consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.Equal(t, uint64(5), highestID)

	// Send packet with valset ID 6 (should be processed)
	changes6 := []abci.ValidatorUpdate{{PubKey: pk2, Power: 60}}
	pd6 := types.NewValidatorSetChangePacketData(changes6, 6)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd6)
	require.NoError(t, err)

	highestID = consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.Equal(t, uint64(6), highestID)

	// Pending changes should now include pk2
	pendingChanges, _ = consumerKeeper.GetPendingChanges(ctx)
	require.Equal(t, 2, len(pendingChanges.ValidatorUpdates))
}

// TestOnRecvVSCPacketDuplicateUpdates tests that the consumer can correctly handle a single VSC packet
// with duplicate valUpdates for the same pub key.
//
// Note: This scenario shouldn't usually happen, ie. the provider shouldn't send duplicate val updates
// for the same pub key. But it's useful to guard against.
func TestOnRecvVSCPacketDuplicateUpdates(t *testing.T) {
	// Arbitrary channel IDs
	consumerCCVChannelID := "consumerCCVChannelID"
	providerCCVChannelID := "providerCCVChannelID"

	// Keeper setup
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	consumerKeeper.SetProviderChannel(ctx, consumerCCVChannelID)
	consumerKeeper.SetParams(ctx, types.DefaultParams())

	// Construct packet/data with duplicate val updates for the same pub key
	cId := crypto.NewCryptoIdentityFromIntSeed(43278947)
	valUpdates := []abci.ValidatorUpdate{
		{
			PubKey: cId.TMProtoCryptoPublicKey(),
			Power:  0,
		},
		{
			PubKey: cId.TMProtoCryptoPublicKey(),
			Power:  473289,
		},
	}
	vscData := types.NewValidatorSetChangePacketData(
		valUpdates,
		1,
	)
	packet := channeltypes.NewPacket(vscData.GetBytes(), 2, types.ProviderPortID,
		providerCCVChannelID, types.ConsumerPortID, consumerCCVChannelID, clienttypes.NewHeight(1, 0), 0)

	// Confirm no pending changes exist before OnRecvVSCPacket
	_, ok := consumerKeeper.GetPendingChanges(ctx)
	require.False(t, ok)

	// Execute OnRecvVSCPacket
	err := consumerKeeper.OnRecvVSCPacket(ctx, packet, vscData)
	require.Nil(t, err)

	// Confirm pending changes are queued by OnRecvVSCPacket
	gotPendingChanges, ok := consumerKeeper.GetPendingChanges(ctx)
	require.True(t, ok)

	// Confirm that only the latest update is kept, duplicate update is discarded
	require.Equal(t, 1, len(gotPendingChanges.ValidatorUpdates))
	require.Equal(t, valUpdates[1], gotPendingChanges.ValidatorUpdates[0]) // Only latest update should be kept
}

