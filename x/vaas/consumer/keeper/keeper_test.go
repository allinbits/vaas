package keeper_test

import (
	"bytes"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"

	abci "github.com/cometbft/cometbft/abci/types"

	"github.com/allinbits/vaas/testutil/crypto"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	"github.com/allinbits/vaas/x/vaas/consumer/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// TestProviderClientID tests getter and setter functionality for the client ID stored on consumer keeper
func TestProviderClientID(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	_, ok := consumerKeeper.GetProviderClientID(ctx)
	require.False(t, ok)
	consumerKeeper.SetProviderClientID(ctx, "someClientID")
	clientID, ok := consumerKeeper.GetProviderClientID(ctx)
	require.True(t, ok)
	require.Equal(t, "someClientID", clientID)
}

func TestConsumerDebtStatus(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	require.False(t, consumerKeeper.IsConsumerInDebt(ctx))

	consumerKeeper.SetConsumerInDebt(ctx, true)
	require.True(t, consumerKeeper.IsConsumerInDebt(ctx))

	consumerKeeper.SetConsumerInDebt(ctx, false)
	require.False(t, consumerKeeper.IsConsumerInDebt(ctx))
}

// TestPendingChanges tests getter, setter, and delete functionality for pending VSCs on a consumer chain
func TestPendingChanges(t *testing.T) {
	pk1, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pk2, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	pd := vaastypes.NewValidatorSetChangePacketData(
		[]abci.ValidatorUpdate{
			{
				PubKey: pk1,
				Power:  30,
			},
			{
				PubKey: pk2,
				Power:  20,
			},
		},
		1,
	)

	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerKeeper.SetPendingChanges(ctx, pd)
	gotPd, ok := consumerKeeper.GetPendingChanges(ctx)
	require.True(t, ok)
	require.Equal(t, &pd, gotPd, "packet data in store does not equal packet data set")
	consumerKeeper.DeletePendingChanges(ctx)
	gotPd, ok = consumerKeeper.GetPendingChanges(ctx)
	require.False(t, ok)
	require.Nil(t, gotPd, "got non-nil pending changes after Delete")
}

// TestLastSovereignHeight tests the getter and setter for the ccv init genesis height
func TestInitGenesisHeight(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	// Panics without setter
	require.Panics(t, func() { consumerKeeper.GetInitGenesisHeight(ctx) })

	// Set/get the height being 10
	consumerKeeper.SetInitGenesisHeight(ctx, 10)
	require.Equal(t, int64(10), consumerKeeper.GetInitGenesisHeight(ctx))

	// Set/get the height being 43234426
	consumerKeeper.SetInitGenesisHeight(ctx, 43234426)
	require.Equal(t, int64(43234426), consumerKeeper.GetInitGenesisHeight(ctx))
}

// TestInitialValSet tests the getter and setter methods for storing the initial validator set for a consumer
func TestInitialValSet(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	// Default value is empty val update list
	require.Empty(t, consumerKeeper.GetInitialValSet(ctx))

	// Set/get the initial validator set
	cId1 := crypto.NewCryptoIdentityFromIntSeed(7896)
	cId2 := crypto.NewCryptoIdentityFromIntSeed(7897)
	cId3 := crypto.NewCryptoIdentityFromIntSeed(7898)
	valUpdates := []abci.ValidatorUpdate{
		{
			PubKey: cId1.TMProtoCryptoPublicKey(),
			Power:  1097,
		},
		{
			PubKey: cId2.TMProtoCryptoPublicKey(),
			Power:  19068,
		},
		{
			PubKey: cId3.TMProtoCryptoPublicKey(),
			Power:  10978554,
		},
	}

	consumerKeeper.SetInitialValSet(ctx, valUpdates)
	require.Equal(t, []abci.ValidatorUpdate{
		{
			PubKey: cId1.TMProtoCryptoPublicKey(),
			Power:  1097,
		},
		{
			PubKey: cId2.TMProtoCryptoPublicKey(),
			Power:  19068,
		},
		{
			PubKey: cId3.TMProtoCryptoPublicKey(),
			Power:  10978554,
		},
	}, consumerKeeper.GetInitialValSet(ctx))
}

// TestCrossChainValidator tests the getter, setter, and deletion method for cross chain validator records
func TestCrossChainValidator(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	// should return false
	_, found := consumerKeeper.GetCCValidator(ctx, ed25519.GenPrivKey().PubKey().Address())
	require.False(t, found)

	// Obtain derived private key
	privKey := ed25519.GenPrivKey()

	// Set cross chain validator
	ccVal, err := types.NewCCValidator(privKey.PubKey().Address(), 1000, privKey.PubKey())
	require.NoError(t, err)
	consumerKeeper.SetCCValidator(ctx, ccVal)

	gotCCVal, found := consumerKeeper.GetCCValidator(ctx, ccVal.Address)
	require.True(t, found)

	// verify the returned validator values
	require.EqualValues(t, ccVal, gotCCVal)

	// expect to return the same consensus pubkey
	pk, err := ccVal.ConsPubKey()
	require.NoError(t, err)
	gotPK, err := gotCCVal.ConsPubKey()
	require.NoError(t, err)
	require.Equal(t, pk, gotPK)

	// delete validator
	consumerKeeper.DeleteCCValidator(ctx, ccVal.Address)

	// should return false
	_, found = consumerKeeper.GetCCValidator(ctx, ccVal.Address)
	require.False(t, found)
}

// TestGetAllCCValidator tests GetAllCCValidator behaviour correctness
func TestGetAllCCValidator(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	ck, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	numValidators := 4
	validators := []types.CrossChainValidator{}
	for range numValidators {
		validators = append(validators, testkeeper.GetNewCrossChainValidator(t))
	}
	// sorting by CrossChainValidator.Address
	expectedGetAllOrder := validators
	sort.Slice(expectedGetAllOrder, func(i, j int) bool {
		return bytes.Compare(expectedGetAllOrder[i].Address, expectedGetAllOrder[j].Address) == -1
	})

	for _, val := range validators {
		ck.SetCCValidator(ctx, val)
	}

	// iterate and check all results are returned in the expected order
	result := ck.GetAllCCValidator(ctx)
	require.Len(t, result, len(validators))
	require.Equal(t, result, expectedGetAllOrder)
}

// TestGetAllHeightToValsetUpdateIDs tests GetAllHeightToValsetUpdateIDs behaviour correctness
func TestGetAllHeightToValsetUpdateIDs(t *testing.T) {
	ck, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cases := []types.HeightToValsetUpdateID{
		{
			ValsetUpdateId: 2,
			Height:         22,
		},
		{
			ValsetUpdateId: 1,
			Height:         11,
		},
		{
			// normal execution should not have two HeightToValsetUpdateID
			// with the same ValsetUpdateId, but let's test it anyway
			ValsetUpdateId: 1,
			Height:         44,
		},
		{
			ValsetUpdateId: 3,
			Height:         33,
		},
	}
	expectedGetAllOrder := cases
	// sorting by Height
	sort.Slice(expectedGetAllOrder, func(i, j int) bool {
		return expectedGetAllOrder[i].Height < expectedGetAllOrder[j].Height
	})

	for _, c := range cases {
		ck.SetHeightValsetUpdateID(ctx, c.Height, c.ValsetUpdateId)
	}

	// iterate and check all results are returned
	result := ck.GetAllHeightToValsetUpdateIDs(ctx)
	require.Len(t, result, len(cases))
	require.Equal(t, expectedGetAllOrder, result)
}
