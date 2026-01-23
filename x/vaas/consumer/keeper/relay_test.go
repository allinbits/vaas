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

