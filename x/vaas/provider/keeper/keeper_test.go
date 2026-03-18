package keeper_test

import (
	"fmt"
	"sort"
	"testing"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	channeltypes "github.com/cosmos/ibc-go/v10/modules/core/04-channel/types"
	ibctesting "github.com/cosmos/ibc-go/v10/testing"
	"github.com/stretchr/testify/require"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	abci "github.com/cometbft/cometbft/abci/types"
	tmprotocrypto "github.com/cometbft/cometbft/proto/tendermint/crypto"
	"go.uber.org/mock/gomock"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

const (
	CONSUMER_CHAIN_ID = "chain-id"
	CONSUMER_ID       = "0"
)

// TestValsetUpdateBlockHeight tests the getter, setter, and deletion methods for valset updates mapped to block height
func TestValsetUpdateBlockHeight(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	blockHeight, found := providerKeeper.GetValsetUpdateBlockHeight(ctx, uint64(0))
	require.False(t, found)
	require.Zero(t, blockHeight)

	providerKeeper.SetValsetUpdateBlockHeight(ctx, uint64(1), uint64(2))
	blockHeight, found = providerKeeper.GetValsetUpdateBlockHeight(ctx, uint64(1))
	require.True(t, found)
	require.Equal(t, blockHeight, uint64(2))

	providerKeeper.DeleteValsetUpdateBlockHeight(ctx, uint64(1))
	blockHeight, found = providerKeeper.GetValsetUpdateBlockHeight(ctx, uint64(1))
	require.False(t, found)
	require.Zero(t, blockHeight)

	providerKeeper.SetValsetUpdateBlockHeight(ctx, uint64(1), uint64(2))
	providerKeeper.SetValsetUpdateBlockHeight(ctx, uint64(3), uint64(4))
	blockHeight, found = providerKeeper.GetValsetUpdateBlockHeight(ctx, uint64(3))
	require.True(t, found)
	require.Equal(t, blockHeight, uint64(4))
}

// TestGetAllValsetUpdateBlockHeights tests GetAllValsetUpdateBlockHeights behaviour correctness
func TestGetAllValsetUpdateBlockHeights(t *testing.T) {
	pk, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cases := []providertypes.ValsetUpdateIdToHeight{
		{
			ValsetUpdateId: 2,
			Height:         22,
		},
		{
			ValsetUpdateId: 1,
			Height:         11,
		},
		{
			// normal execution should not have two ValsetUpdateIdToHeight
			// with the same Height, but let's test it anyway
			ValsetUpdateId: 4,
			Height:         11,
		},
		{
			ValsetUpdateId: 3,
			Height:         33,
		},
	}
	expectedGetAllOrder := cases
	// sorting by ValsetUpdateId
	sort.Slice(expectedGetAllOrder, func(i, j int) bool {
		return expectedGetAllOrder[i].ValsetUpdateId < expectedGetAllOrder[j].ValsetUpdateId
	})

	for _, c := range cases {
		pk.SetValsetUpdateBlockHeight(ctx, c.ValsetUpdateId, c.Height)
	}

	// iterate and check all results are returned in the expected order
	result := pk.GetAllValsetUpdateBlockHeights(ctx)
	require.Len(t, result, len(cases))
	require.Equal(t, expectedGetAllOrder, result)
}

// TestPendingVSCs tests the getter, appending, and deletion methods for stored pending VSCs
func TestPendingVSCs(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	chainID := CONSUMER_CHAIN_ID

	pending := providerKeeper.GetPendingVSCPackets(ctx, chainID)
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
	providerKeeper.AppendPendingVSCPackets(ctx, chainID, packetList...)

	packets := providerKeeper.GetPendingVSCPackets(ctx, chainID)
	require.Len(t, packets, 2)

	newPacket := vaastypes.ValidatorSetChangePacketData{
		ValidatorUpdates: []abci.ValidatorUpdate{
			{PubKey: ppks[3], Power: 4},
		},
		ValsetUpdateId: 3,
	}
	providerKeeper.AppendPendingVSCPackets(ctx, chainID, newPacket)
	vscs := providerKeeper.GetPendingVSCPackets(ctx, chainID)
	require.Len(t, vscs, 3)
	require.True(t, vscs[len(vscs)-1].ValsetUpdateId == 3)
	require.True(t, vscs[len(vscs)-1].GetValidatorUpdates()[0].PubKey.String() == ppks[3].String())

	providerKeeper.DeletePendingVSCPackets(ctx, chainID)
	pending = providerKeeper.GetPendingVSCPackets(ctx, chainID)
	require.Len(t, pending, 0)
}

func TestConsumerDebtStatus(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerID := CONSUMER_ID

	require.False(t, providerKeeper.IsConsumerInDebt(ctx, consumerID))
	require.False(t, providerKeeper.HasPendingConsumerDebtPacket(ctx, consumerID))

	providerKeeper.SetConsumerInDebt(ctx, consumerID, true)
	providerKeeper.SetPendingConsumerDebtPacket(ctx, consumerID)
	require.True(t, providerKeeper.IsConsumerInDebt(ctx, consumerID))
	require.True(t, providerKeeper.HasPendingConsumerDebtPacket(ctx, consumerID))

	providerKeeper.DeleteConsumerDebt(ctx, consumerID)
	providerKeeper.ClearPendingConsumerDebtPacket(ctx, consumerID)
	require.False(t, providerKeeper.IsConsumerInDebt(ctx, consumerID))
	require.False(t, providerKeeper.HasPendingConsumerDebtPacket(ctx, consumerID))
}

func TestQueuePendingConsumerDebtPackets(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerID := CONSUMER_ID
	providerKeeper.SetConsumerClientId(ctx, consumerID, "client-0")
	providerKeeper.SetConsumerIdToChannelId(ctx, consumerID, "channel-0")
	providerKeeper.SetConsumerPhase(ctx, consumerID, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerInDebt(ctx, consumerID, true)
	providerKeeper.SetPendingConsumerDebtPacket(ctx, consumerID)

	queued := providerKeeper.QueuePendingConsumerDebtPackets(ctx, 7)

	require.True(t, queued)
	require.False(t, providerKeeper.HasPendingConsumerDebtPacket(ctx, consumerID))

	packets := providerKeeper.GetPendingVSCPackets(ctx, consumerID)
	require.Len(t, packets, 1)
	require.Equal(t, uint64(7), packets[0].ValsetUpdateId)
	require.True(t, packets[0].ConsumerInDebt)
	require.Empty(t, packets[0].ValidatorUpdates)
}

func TestEndBlockVSUAssignsUniqueValsetUpdateIDToDebtPacket(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	ctx = ctx.WithBlockHeight(1)

	consumerID := CONSUMER_ID
	channelID := "channel-0"

	providerKeeper.SetConsumerClientId(ctx, consumerID, "client-0")
	providerKeeper.SetConsumerIdToChannelId(ctx, consumerID, channelID)
	providerKeeper.SetConsumerPhase(ctx, consumerID, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerInDebt(ctx, consumerID, true)
	providerKeeper.SetPendingConsumerDebtPacket(ctx, consumerID)

	require.Equal(t, providertypes.DefaultValsetUpdateID, providerKeeper.GetValidatorSetUpdateId(ctx))

	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(ctx).
		Return([]stakingtypes.Validator{}, nil)
	mocks.MockChannelKeeper.EXPECT().
		GetChannel(ctx, vaastypes.ProviderPortID, channelID).
		Return(channeltypes.Channel{}, true)
	mocks.MockChannelKeeper.EXPECT().
		SendPacket(ctx, vaastypes.ProviderPortID, channelID, gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ sdk.Context, sourcePort, sourceChannel string, _ clienttypes.Height, _ uint64, data []byte) (uint64, error) {
			require.Equal(t, vaastypes.ProviderPortID, sourcePort)
			require.Equal(t, channelID, sourceChannel)

			var packet vaastypes.ValidatorSetChangePacketData
			require.NoError(t, vaastypes.ModuleCdc.UnmarshalJSON(data, &packet))
			require.Equal(t, providertypes.DefaultValsetUpdateID, packet.ValsetUpdateId)
			require.True(t, packet.ConsumerInDebt)
			require.Empty(t, packet.ValidatorUpdates)

			return uint64(1), nil
		})

	valUpdates, err := providerKeeper.EndBlockVSU(ctx)
	require.NoError(t, err)
	require.Empty(t, valUpdates)
	require.Equal(t, providertypes.DefaultValsetUpdateID+1, providerKeeper.GetValidatorSetUpdateId(ctx))
	require.Empty(t, providerKeeper.GetPendingVSCPackets(ctx, consumerID))
}

// TestInitHeight tests the getter and setter methods for the stored block heights (on provider) when a given consumer chain was started
func TestInitHeight(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	tc := []struct {
		chainID  string
		expected uint64
	}{
		{expected: 0, chainID: "chain"},
		{expected: 10, chainID: "chain1"},
		{expected: 12, chainID: "chain2"},
	}

	providerKeeper.SetInitChainHeight(ctx, tc[1].chainID, tc[1].expected)
	providerKeeper.SetInitChainHeight(ctx, tc[2].chainID, tc[2].expected)

	for _, tc := range tc {
		height, _ := providerKeeper.GetInitChainHeight(ctx, tc.chainID)
		require.Equal(t, tc.expected, height)
	}
}

func TestGetAllConsumersWithIBCClients(t *testing.T) {
	pk, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerIds := []string{"2", "1", "4", "3"}
	for i, consumerId := range consumerIds {
		clientId := fmt.Sprintf("client-%d", len(consumerIds)-i)
		pk.SetConsumerClientId(ctx, consumerId, clientId)
		pk.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	}

	actualConsumerIds := pk.GetAllConsumersWithIBCClients(ctx)
	require.Len(t, actualConsumerIds, len(consumerIds))

	// sort the consumer ids before comparing they are equal
	sort.Slice(consumerIds, func(i, j int) bool {
		return consumerIds[i] < consumerIds[j]
	})
	sort.Slice(actualConsumerIds, func(i, j int) bool {
		return actualConsumerIds[i] < actualConsumerIds[j]
	})
	require.Equal(t, consumerIds, actualConsumerIds)
}

// TestGetAllChannelToChains tests GetAllChannelToConsumers behaviour correctness
func TestGetAllChannelToChains(t *testing.T) {
	pk, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerIds := []string{"2", "1", "4", "3"}
	var expectedGetAllOrder []struct {
		ChannelId  string
		ConsumerId string
	}

	for i, consumerId := range consumerIds {
		channelID := fmt.Sprintf("client-%d", len(consumerIds)-i)
		pk.SetChannelToConsumerId(ctx, channelID, consumerId)
		expectedGetAllOrder = append(expectedGetAllOrder, struct {
			ChannelId  string
			ConsumerId string
		}{ConsumerId: consumerId, ChannelId: channelID})
	}
	// sorting by channelID
	sort.Slice(expectedGetAllOrder, func(i, j int) bool {
		return expectedGetAllOrder[i].ChannelId < expectedGetAllOrder[j].ChannelId
	})

	result := pk.GetAllChannelToConsumers(ctx)
	require.Len(t, result, len(consumerIds))
	require.Equal(t, expectedGetAllOrder, result)
}

// TestConsumerClientId tests the getter, setter, and deletion of the client id <> consumer id mappings
func TestConsumerClientId(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := "123"
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
	res, found = providerKeeper.GetClientIdToConsumerId(ctx, clientIds[0])
	require.True(t, found)
	require.Equal(t, consumerId, res)
	_, found = providerKeeper.GetClientIdToConsumerId(ctx, clientIds[1])
	require.False(t, found)

	// overwrite the client ID
	providerKeeper.SetConsumerClientId(ctx, consumerId, clientIds[1])
	res, found = providerKeeper.GetConsumerClientId(ctx, consumerId)
	require.True(t, found)
	require.Equal(t, clientIds[1], res)
	res, found = providerKeeper.GetClientIdToConsumerId(ctx, clientIds[1])
	require.True(t, found)
	require.Equal(t, consumerId, res)
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
