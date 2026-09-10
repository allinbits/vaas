package keeper_test

import (
	"testing"

	ibctesting "github.com/cosmos/ibc-go/v10/testing"
	"github.com/stretchr/testify/require"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"

	abci "github.com/cometbft/cometbft/abci/types"
	tmprotocrypto "github.com/cometbft/cometbft/proto/tendermint/crypto"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

const (
	CONSUMER_ID uint64 = 0
)

// TestPendingVSCs tests the getter, appending, and deletion methods for stored pending VSCs
func TestPendingVSCs(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerID := CONSUMER_ID

	pending := providerKeeper.GetPendingVSCPackets(ctx, consumerID)
	require.Len(t, pending, 0)

	_, pks, _ := ibctesting.GenerateKeys(t, 4)
	var ppks [4]tmprotocrypto.PublicKey
	for i, pk := range pks {
		ppks[i], _ = cryptocodec.ToCmtProtoPublicKey(pk)
	}

	packetList := []vaastypes.ValidatorSetChangePacketData{
		{
			ValidatorUpdates: []abci.ValidatorUpdate{
				{PubKey: ppks[0], Power: 1},
				{PubKey: ppks[1], Power: 2},
			},
			ValsetUpdateId: 1,
		},
		{
			ValidatorUpdates: []abci.ValidatorUpdate{
				{PubKey: ppks[2], Power: 3},
			},
			ValsetUpdateId: 2,
		},
	}
	providerKeeper.AppendPendingVSCPackets(ctx, consumerID, packetList...)

	packets := providerKeeper.GetPendingVSCPackets(ctx, consumerID)
	require.Len(t, packets, 2)

	newPacket := vaastypes.ValidatorSetChangePacketData{
		ValidatorUpdates: []abci.ValidatorUpdate{
			{PubKey: ppks[3], Power: 4},
		},
		ValsetUpdateId: 3,
	}
	providerKeeper.AppendPendingVSCPackets(ctx, consumerID, newPacket)
	vscs := providerKeeper.GetPendingVSCPackets(ctx, consumerID)
	require.Len(t, vscs, 3)
	require.True(t, vscs[len(vscs)-1].ValsetUpdateId == 3)
	require.True(t, vscs[len(vscs)-1].GetValidatorUpdates()[0].PubKey.String() == ppks[3].String())

	providerKeeper.DeletePendingVSCPackets(ctx, consumerID)
	pending = providerKeeper.GetPendingVSCPackets(ctx, consumerID)
	require.Len(t, pending, 0)
}

func TestConsumerDebtStatus(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerID := CONSUMER_ID

	require.False(t, providerKeeper.IsConsumerInDebt(ctx, consumerID))

	providerKeeper.SetConsumerInDebt(ctx, consumerID, true)
	require.True(t, providerKeeper.IsConsumerInDebt(ctx, consumerID))

	providerKeeper.DeleteConsumerDebt(ctx, consumerID)
	require.False(t, providerKeeper.IsConsumerInDebt(ctx, consumerID))
}

// TestInitHeight tests the getter and setter methods for the stored block heights (on provider) when a given consumer chain was started
func TestInitHeight(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	tc := []struct {
		consumerID uint64
		expected   uint64
	}{
		{expected: 0, consumerID: 0},
		{expected: 10, consumerID: 1},
		{expected: 12, consumerID: 2},
	}

	providerKeeper.SetInitChainHeight(ctx, tc[1].consumerID, tc[1].expected)
	providerKeeper.SetInitChainHeight(ctx, tc[2].consumerID, tc[2].expected)

	for _, tc := range tc {
		height, _ := providerKeeper.GetInitChainHeight(ctx, tc.consumerID)
		require.Equal(t, tc.expected, height)
	}
}

// TestConsumerClientId tests the getter, setter, and deletion of the client id <> consumer id mappings
func TestConsumerClientId(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(123)
	clientIds := []string{"clientId1", "clientId2"}

	_, found := providerKeeper.GetConsumerClientId(ctx, consumerId)
	require.False(t, found)
	_, found = providerKeeper.GetClientIdToConsumerId(ctx, clientIds[0])
	require.False(t, found)
	_, found = providerKeeper.GetClientIdToConsumerId(ctx, clientIds[1])
	require.False(t, found)

	providerKeeper.SetConsumerClientId(ctx, consumerId, clientIds[0])
	res, found := providerKeeper.GetConsumerClientId(ctx, consumerId)
	require.True(t, found)
	require.Equal(t, clientIds[0], res)
	gotCid, found := providerKeeper.GetClientIdToConsumerId(ctx, clientIds[0])
	require.True(t, found)
	require.Equal(t, consumerId, gotCid)
	_, found = providerKeeper.GetClientIdToConsumerId(ctx, clientIds[1])
	require.False(t, found)

	// overwrite the client ID
	providerKeeper.SetConsumerClientId(ctx, consumerId, clientIds[1])
	res, found = providerKeeper.GetConsumerClientId(ctx, consumerId)
	require.True(t, found)
	require.Equal(t, clientIds[1], res)
	gotCid, found = providerKeeper.GetClientIdToConsumerId(ctx, clientIds[1])
	require.True(t, found)
	require.Equal(t, consumerId, gotCid)
	_, found = providerKeeper.GetClientIdToConsumerId(ctx, clientIds[0])
	require.False(t, found)

	providerKeeper.DeleteConsumerClientId(ctx, consumerId)
	_, found = providerKeeper.GetConsumerClientId(ctx, consumerId)
	require.False(t, found)
	_, found = providerKeeper.GetClientIdToConsumerId(ctx, clientIds[0])
	require.False(t, found)
	_, found = providerKeeper.GetClientIdToConsumerId(ctx, clientIds[1])
	require.False(t, found)
}
