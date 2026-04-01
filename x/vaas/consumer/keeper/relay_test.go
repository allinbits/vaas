package keeper_test

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"

	abci "github.com/cometbft/cometbft/abci/types"

	testcrypto "github.com/allinbits/vaas/testutil/crypto"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	"github.com/allinbits/vaas/x/vaas/types"
)

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

	pd1 := types.NewValidatorSetChangePacketData(changes1, 1)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd1)
	require.NoError(t, err, "first packet should succeed")

	clientID, found := consumerKeeper.GetProviderClientID(ctx)
	require.True(t, found)
	require.Equal(t, providerClientID, clientID)

	pendingChanges, ok := consumerKeeper.GetPendingChanges(ctx)
	require.True(t, ok)
	require.Equal(t, 2, len(pendingChanges.ValidatorUpdates))

	highestID, _, err := consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(1), highestID)

	pd2 := types.NewValidatorSetChangePacketData(changes2, 2)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd2)
	require.NoError(t, err, "second packet should succeed")

	highestID, _, err = consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(2), highestID)

	differentClientID := "07-tendermint-999"
	pd3 := types.NewValidatorSetChangePacketData(changes1, 3)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, differentClientID, pd3)
	require.Error(t, err, "packet from different client should fail")

	highestID, _, err = consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(2), highestID)
}

func TestOnRecvVSCPacketV2OutOfOrder(t *testing.T) {
	providerClientID := "07-tendermint-0"

	pk1, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pk2, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	changes5 := []abci.ValidatorUpdate{{PubKey: pk1, Power: 50}}
	pd5 := types.NewValidatorSetChangePacketData(changes5, 5)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd5)
	require.NoError(t, err)

	highestID, _, err := consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(5), highestID)

	pendingChanges, _ := consumerKeeper.GetPendingChanges(ctx)
	require.Equal(t, int64(50), pendingChanges.ValidatorUpdates[0].Power)

	changes3 := []abci.ValidatorUpdate{{PubKey: pk2, Power: 30}}
	pd3 := types.NewValidatorSetChangePacketData(changes3, 3)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd3)
	require.NoError(t, err, "out-of-order packet should be acknowledged without error")

	highestID, _, err = consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(5), highestID)

	pendingChanges, _ = consumerKeeper.GetPendingChanges(ctx)
	require.Equal(t, 1, len(pendingChanges.ValidatorUpdates))
	require.Equal(t, int64(50), pendingChanges.ValidatorUpdates[0].Power)

	changes6 := []abci.ValidatorUpdate{{PubKey: pk2, Power: 60}}
	pd6 := types.NewValidatorSetChangePacketData(changes6, 6)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd6)
	require.NoError(t, err)

	highestID, _, err = consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(6), highestID)

	pendingChanges, _ = consumerKeeper.GetPendingChanges(ctx)
	require.Equal(t, 2, len(pendingChanges.ValidatorUpdates))
}

func TestOnRecvVSCPacketV2FirstPacketNotDropped(t *testing.T) {
	providerClientID := "07-tendermint-0"

	pk1, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	_, found, err := consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.False(t, found, "unset HighestValsetUpdateID should return found=false")

	changes := []abci.ValidatorUpdate{{PubKey: pk1, Power: 100}}
	pd1 := types.NewValidatorSetChangePacketData(changes, 1)

	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd1)
	require.NoError(t, err, "first packet should be processed when no highest ID is set")

	pendingChanges, ok := consumerKeeper.GetPendingChanges(ctx)
	require.True(t, ok)
	require.Equal(t, int64(100), pendingChanges.ValidatorUpdates[0].Power)

	highestID, found, err := consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint64(1), highestID)
}

func TestOnRecvVSCPacketV2AccumulatesChanges(t *testing.T) {
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

	pd1 := types.NewValidatorSetChangePacketData(changes1, 1)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd1)
	require.NoError(t, err)

	pd2 := types.NewValidatorSetChangePacketData(changes2, 2)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd2)
	require.NoError(t, err)

	pendingChanges, ok := consumerKeeper.GetPendingChanges(ctx)
	require.True(t, ok)

	expected := types.ValidatorSetChangePacketData{ValidatorUpdates: []abci.ValidatorUpdate{
		{PubKey: pk1, Power: 30},
		{PubKey: pk2, Power: 40},
		{PubKey: pk3, Power: 10},
	}}

	sort.SliceStable(pendingChanges.ValidatorUpdates, func(i, j int) bool {
		return pendingChanges.ValidatorUpdates[i].PubKey.Compare(pendingChanges.ValidatorUpdates[j].PubKey) == -1
	})
	sort.SliceStable(expected.ValidatorUpdates, func(i, j int) bool {
		return expected.ValidatorUpdates[i].PubKey.Compare(expected.ValidatorUpdates[j].PubKey) == -1
	})
	require.Equal(t, expected, *pendingChanges)
}

func TestOnRecvVSCPacketV2DuplicateUpdates(t *testing.T) {
	providerClientID := "07-tendermint-0"

	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cId := testcrypto.NewCryptoIdentityFromIntSeed(43278947)
	valUpdates := []abci.ValidatorUpdate{
		{PubKey: cId.TMProtoCryptoPublicKey(), Power: 0},
		{PubKey: cId.TMProtoCryptoPublicKey(), Power: 473289},
	}
	vscData := types.NewValidatorSetChangePacketData(valUpdates, 1)

	err := consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, vscData)
	require.NoError(t, err)

	gotPendingChanges, ok := consumerKeeper.GetPendingChanges(ctx)
	require.True(t, ok)

	require.Equal(t, 1, len(gotPendingChanges.ValidatorUpdates))
	require.Equal(t, valUpdates[1], gotPendingChanges.ValidatorUpdates[0])
}
