package keeper_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	commitmenttypes "github.com/cosmos/ibc-go/v10/modules/core/23-commitment/types"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	"cosmossdk.io/math"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	abci "github.com/cometbft/cometbft/abci/types"
	tmtypes "github.com/cometbft/cometbft/types"

	"github.com/allinbits/vaas/testutil/crypto"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	consumerkeeper "github.com/allinbits/vaas/x/vaas/consumer/keeper"
	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// TestInitGenesis tests that a consumer chain is correctly initialised from genesis.
// It covers the start of a new chain, the restart of a chain during the CCV channel handshake
// and finally the restart of chain when the CCV channel is already established.
func TestInitGenesis(t *testing.T) {
	// mock the consumer genesis state values
	provClientID := "tendermint-07"
	provClientType := "07-tendermint"

	vscID := uint64(0)
	blockHeight := uint64(0)

	// create validator set
	cId := crypto.NewCryptoIdentityFromIntSeed(234234)
	pubKey := cId.TMCryptoPubKey()
	validator := tmtypes.NewValidator(pubKey, 1)
	valset := []abci.ValidatorUpdate{tmtypes.TM2PB.ValidatorUpdate(validator)}

	// create ibc client and last consensus states
	provConsState := ibctmtypes.NewConsensusState(
		time.Time{},
		commitmenttypes.NewMerkleRoot([]byte("apphash")),
		tmtypes.NewValidatorSet([]*tmtypes.Validator{validator}).Hash(),
	)

	provClientState := ibctmtypes.NewClientState(
		"provider",
		ibctmtypes.DefaultTrustLevel,
		0,
		stakingtypes.DefaultUnbondingTime,
		time.Second*10,
		clienttypes.Height{},
		commitmenttypes.GetSDKSpecs(),
		[]string{"upgrade", "upgradedIBCState"},
	)

	// mock height to valset update ID values
	defaultHeightValsetUpdateIDs := []consumertypes.HeightToValsetUpdateID{
		{ValsetUpdateId: vscID, Height: blockHeight},
	}

	params := vaastypes.DefaultConsumerParams()
	params.Enabled = true

	testCases := []struct {
		name         string
		malleate     func(sdk.Context, testkeeper.MockedKeepers)
		genesis      *consumertypes.GenesisState
		assertStates func(sdk.Context, consumerkeeper.Keeper, *consumertypes.GenesisState)
	}{
		{
			"start a new chain",
			func(ctx sdk.Context, mocks testkeeper.MockedKeepers) {
				clientStateBytes, err := provClientState.Marshal()
				require.NoError(t, err)
				consStateBytes, err := provConsState.Marshal()
				require.NoError(t, err)
				gomock.InOrder(
					testkeeper.ExpectCreateClientMock(ctx, mocks, provClientType, provClientID, clientStateBytes,
						consStateBytes),
				)
			},
			consumertypes.NewInitialGenesisState(
				provClientState,
				provConsState,
				valset,
				params,
			),
			func(ctx sdk.Context, ck consumerkeeper.Keeper, gs *consumertypes.GenesisState) {
				assertProviderClientID(t, ctx, &ck, provClientID)
				assertHeightValsetUpdateIDs(t, ctx, &ck, defaultHeightValsetUpdateIDs)

				require.Equal(t, validator.Address.Bytes(), ck.GetAllCCValidator(ctx)[0].Address)
				require.Equal(t, gs.Params, ck.GetConsumerParams(ctx))
			},
		}, {
			"restart a chain without an established CCV channel",
			func(ctx sdk.Context, mocks testkeeper.MockedKeepers) {
			},
			consumertypes.NewRestartGenesisState(
				provClientID,
				valset,
				defaultHeightValsetUpdateIDs,
				params,
			),
			func(ctx sdk.Context, ck consumerkeeper.Keeper, gs *consumertypes.GenesisState) {
				assertHeightValsetUpdateIDs(t, ctx, &ck, defaultHeightValsetUpdateIDs)
				assertProviderClientID(t, ctx, &ck, provClientID)
				require.Equal(t, validator.Address.Bytes(), ck.GetAllCCValidator(ctx)[0].Address)
				require.Equal(t, gs.Params, ck.GetConsumerParams(ctx))
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			keeperParams := testkeeper.NewInMemKeeperParams(t)
			consumerKeeper, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, keeperParams)
			defer ctrl.Finish()

			tc.malleate(ctx, mocks)

			consumerKeeper.InitGenesis(ctx, tc.genesis)

			tc.assertStates(ctx, consumerKeeper, tc.genesis)
		})
	}
}

func TestExportGenesis(t *testing.T) {
	provClientID := "tendermint-07"

	vscID := uint64(0)
	blockHeight := uint64(0)

	pubKey := ed25519.GenPrivKey().PubKey()
	tmPK, err := cryptocodec.ToCmtPubKeyInterface(pubKey)
	require.NoError(t, err)
	validator := tmtypes.NewValidator(tmPK, 1)
	valset := []abci.ValidatorUpdate{tmtypes.TM2PB.ValidatorUpdate(validator)}

	defaultHeightValsetUpdateIDs := []consumertypes.HeightToValsetUpdateID{
		{ValsetUpdateId: vscID, Height: blockHeight},
	}
	params := vaastypes.DefaultConsumerParams()
	params.Enabled = true

	testCases := []struct {
		name       string
		malleate   func(sdk.Context, consumerkeeper.Keeper, testkeeper.MockedKeepers)
		expGenesis *consumertypes.GenesisState
	}{
		{
			"export a chain without an established CCV channel",
			func(ctx sdk.Context, ck consumerkeeper.Keeper, mocks testkeeper.MockedKeepers) {
				ck.SetProviderClientID(ctx, provClientID)
				cVal, err := consumertypes.NewCCValidator(validator.Address.Bytes(), 1, pubKey)
				require.NoError(t, err)
				ck.SetCCValidator(ctx, cVal)
				ck.SetParams(ctx, params)

				ck.SetHeightValsetUpdateID(ctx, defaultHeightValsetUpdateIDs[0].Height, defaultHeightValsetUpdateIDs[0].ValsetUpdateId)
			},
			consumertypes.NewRestartGenesisState(
				provClientID,
				valset,
				defaultHeightValsetUpdateIDs,
				params,
			),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			keeperParams := testkeeper.NewInMemKeeperParams(t)
			consumerKeeper, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, keeperParams)
			defer ctrl.Finish()
			consumerKeeper.SetParams(ctx, params)

			tc.malleate(ctx, consumerKeeper, mocks)

			gotGen := consumerKeeper.ExportGenesis(ctx)

			require.EqualValues(t, tc.expGenesis, gotGen)
		})
	}
}

// TestGenesisRoundTripLastVSCRecvTime verifies the consumer's VSC-staleness
// clock survives an export/import restart: ExportGenesis carries the recorded
// last-VSC-recv time, and InitGenesis restores it on a fresh keeper (rather than
// falling back to the current block time, which would reset the safe-mode clock).
func TestGenesisRoundTripLastVSCRecvTime(t *testing.T) {
	provClientID := "tendermint-07"
	params := vaastypes.DefaultConsumerParams()
	params.Enabled = true

	pubKey := ed25519.GenPrivKey().PubKey()
	tmPK, err := cryptocodec.ToCmtPubKeyInterface(pubKey)
	require.NoError(t, err)
	validator := tmtypes.NewValidator(tmPK, 1)

	lastRecv := time.Unix(1_850_000_000, 0).UTC()

	// Export half: a keeper with a recorded last-VSC-recv time exports it.
	ck, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ck.SetParams(ctx, params)
	ck.SetProviderClientID(ctx, provClientID)
	cVal, err := consumertypes.NewCCValidator(validator.Address.Bytes(), 1, pubKey)
	require.NoError(t, err)
	ck.SetCCValidator(ctx, cVal)
	ck.SetHeightValsetUpdateID(ctx, 0, 0)
	ck.SetLastVSCRecvTime(ctx, lastRecv)

	exported := ck.ExportGenesis(ctx)
	require.NotNil(t, exported.LastVscRecvTime, "export must carry last_vsc_recv_time")
	require.Equal(t, lastRecv, *exported.LastVscRecvTime)

	// Import half: a fresh keeper restores the exact time, not the block-time fallback.
	ck2, ctx2, ctrl2, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl2.Finish()
	ck2.InitGenesis(ctx2, exported)
	require.Equal(t, lastRecv, ck2.GetLastVSCRecvTime(ctx2))
}

// TestGenesisRoundTripDowntimeState verifies that the consumer's
// downtime-detection state (in-progress missed-block bitmaps, first-tracked
// heights, staged downtime params, and queued evidence packets) survives an
// export/import restart.
func TestGenesisRoundTripDowntimeState(t *testing.T) {
	provClientID := "tendermint-07"
	params := vaastypes.DefaultConsumerParams()
	params.Enabled = true

	pubKey := ed25519.GenPrivKey().PubKey()
	tmPK, err := cryptocodec.ToCmtPubKeyInterface(pubKey)
	require.NoError(t, err)
	validator := tmtypes.NewValidator(tmPK, 1)

	addr1 := []byte("validator-addr-downtime-one")
	addr2 := []byte("validator-addr-downtime-two")
	bitmap1 := []byte{0xFF, 0x00}
	bitmap2 := []byte{0x0F, 0xF0}
	staged := vaastypes.DowntimeParams{
		SignedBlocksWindow: 200,
		MinSignedPerWindow: math.LegacyNewDecWithPrec(6, 1),
	}
	evPacket := vaastypes.NewEvidencePacketData(
		sdk.ConsAddress(addr1), 1, []byte{0xFF, 0x03}, 10, 100, math.LegacyNewDecWithPrec(5, 1),
	)

	ck, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ck.SetParams(ctx, params)
	ck.SetProviderClientID(ctx, provClientID)
	cVal, err := consumertypes.NewCCValidator(validator.Address.Bytes(), 1, pubKey)
	require.NoError(t, err)
	ck.SetCCValidator(ctx, cVal)
	ck.SetHeightValsetUpdateID(ctx, 0, 0)

	require.NoError(t, ck.MissedBlockBitmaps.Set(ctx, addr1, bitmap1))
	require.NoError(t, ck.MissedBlockBitmaps.Set(ctx, addr2, bitmap2))
	require.NoError(t, ck.FirstTrackedHeights.Set(ctx, addr1, 10))
	require.NoError(t, ck.FirstTrackedHeights.Set(ctx, addr2, 20))
	require.NoError(t, ck.StagedDowntimeParams.Set(ctx, staged))
	require.NoError(t, ck.QueueEvidencePacket(ctx, evPacket))

	exported := ck.ExportGenesis(ctx)
	require.Len(t, exported.MissedBlockBitmaps, 2)
	require.Len(t, exported.FirstTrackedHeights, 2)
	require.NotNil(t, exported.StagedDowntimeParams)
	require.Equal(t, staged, *exported.StagedDowntimeParams)
	require.Len(t, exported.PendingEvidencePackets, 1)
	require.Equal(t, addr1, exported.PendingEvidencePackets[0].Addr)
	require.Equal(t, evPacket.GetBytes(), exported.PendingEvidencePackets[0].Packet)

	ck2, ctx2, ctrl2, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl2.Finish()
	ck2.InitGenesis(ctx2, exported)

	gotBitmap1, err := ck2.MissedBlockBitmaps.Get(ctx2, addr1)
	require.NoError(t, err, "MissedBlockBitmaps lost across round-trip")
	require.Equal(t, bitmap1, gotBitmap1)
	gotBitmap2, err := ck2.MissedBlockBitmaps.Get(ctx2, addr2)
	require.NoError(t, err, "MissedBlockBitmaps lost across round-trip")
	require.Equal(t, bitmap2, gotBitmap2)

	gotHeight1, err := ck2.FirstTrackedHeights.Get(ctx2, addr1)
	require.NoError(t, err, "FirstTrackedHeights lost across round-trip")
	require.Equal(t, int64(10), gotHeight1)
	gotHeight2, err := ck2.FirstTrackedHeights.Get(ctx2, addr2)
	require.NoError(t, err, "FirstTrackedHeights lost across round-trip")
	require.Equal(t, int64(20), gotHeight2)

	gotStaged, err := ck2.StagedDowntimeParams.Get(ctx2)
	require.NoError(t, err, "StagedDowntimeParams lost across round-trip")
	require.Equal(t, staged, gotStaged)

	gotPacket, err := ck2.PendingEvidencePackets.Get(ctx2, addr1)
	require.NoError(t, err, "PendingEvidencePackets lost across round-trip")
	require.Equal(t, evPacket.GetBytes(), gotPacket)

	reExported := ck2.ExportGenesis(ctx2)
	require.Equal(t, exported, reExported, "round-trip must be a fixed point")

	// A genesis whose queued packet bytes are corrupt must be rejected by
	// Validate before InitGenesis ever hands them to the keeper.
	corrupt := consumertypes.NewRestartGenesisState(
		provClientID,
		exported.Provider.InitialValSet,
		exported.HeightToValsetUpdateId,
		params,
	)
	corrupt.PendingEvidencePackets = []consumertypes.PendingEvidencePacketEntry{
		{Addr: addr1, Packet: []byte("{not json")},
	}
	require.Error(t, corrupt.Validate())

	corrupt.PendingEvidencePackets = []consumertypes.PendingEvidencePacketEntry{
		{Addr: addr1, Packet: evPacket.GetBytes()},
	}
	require.NoError(t, corrupt.Validate())
}

// TestGenesisRoundTripProviderChainId verifies the consumer's pinned
// provider chain id survives an export/import restart: ExportGenesis
// carries the pinned chain id, and InitGenesis's restart branch restores it
// on a fresh keeper rather than leaving it unset until the next VSC packet
// lazily re-establishes it via authenticateProviderChainID.
func TestGenesisRoundTripProviderChainId(t *testing.T) {
	provClientID := "tendermint-07"
	params := vaastypes.DefaultConsumerParams()
	params.Enabled = true

	pubKey := ed25519.GenPrivKey().PubKey()
	tmPK, err := cryptocodec.ToCmtPubKeyInterface(pubKey)
	require.NoError(t, err)
	validator := tmtypes.NewValidator(tmPK, 1)

	providerChainId := "cosmoshub-4"

	ck, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ck.SetParams(ctx, params)
	ck.SetProviderClientID(ctx, provClientID)
	cVal, err := consumertypes.NewCCValidator(validator.Address.Bytes(), 1, pubKey)
	require.NoError(t, err)
	ck.SetCCValidator(ctx, cVal)
	ck.SetHeightValsetUpdateID(ctx, 0, 0)
	ck.SetProviderChainId(ctx, providerChainId)

	exported := ck.ExportGenesis(ctx)
	require.Equal(t, providerChainId, exported.ProviderChainId, "export must carry provider_chain_id")
	require.False(t, exported.NewChain, "restart export must not be a new-chain genesis")

	ck2, ctx2, ctrl2, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl2.Finish()
	ck2.InitGenesis(ctx2, exported)

	gotChainId, ok := ck2.GetProviderChainId(ctx2)
	require.True(t, ok, "ProviderChainId lost across round-trip")
	require.Equal(t, providerChainId, gotChainId)

	reExported := ck2.ExportGenesis(ctx2)
	require.Equal(t, exported, reExported, "round-trip must be a fixed point")
}

func assertProviderClientID(t *testing.T, ctx sdk.Context, ck *consumerkeeper.Keeper, clientID string) {
	t.Helper()
	cid, ok := ck.GetProviderClientID(ctx)
	require.True(t, ok)
	require.Equal(t, clientID, cid)
}

func assertHeightValsetUpdateIDs(t *testing.T, ctx sdk.Context, ck *consumerkeeper.Keeper, heighValsetUpdateIDs []consumertypes.HeightToValsetUpdateID) {
	t.Helper()
	ctr := 0

	for _, heightToValsetUpdateID := range ck.GetAllHeightToValsetUpdateIDs(ctx) {
		require.Equal(t, heighValsetUpdateIDs[ctr].Height, heightToValsetUpdateID.Height)
		require.Equal(t, heighValsetUpdateIDs[ctr].ValsetUpdateId, heightToValsetUpdateID.ValsetUpdateId)
		ctr++
	}
}

func TestHighestValsetUpdateID(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	highestID, found, err := consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.False(t, found)
	require.Equal(t, uint64(0), highestID)

	consumerKeeper.SetHighestValsetUpdateID(ctx, 5)
	highestID, found, err = consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint64(5), highestID)

	consumerKeeper.SetHighestValsetUpdateID(ctx, 10)
	highestID, found, err = consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint64(10), highestID)

	consumerKeeper.SetHighestValsetUpdateID(ctx, 3)
	highestID, found, err = consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint64(3), highestID)
}
