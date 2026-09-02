package keeper_test

import (
	"fmt"
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

// expectProviderClientExists satisfies the restart-arm guard that the pinned
// provider client must exist in the IBC client store.
func expectProviderClientExists(mocks testkeeper.MockedKeepers) {
	mocks.MockClientKeeper.EXPECT().GetClientState(gomock.Any(), gomock.Any()).Return(nil, true).AnyTimes()
}

func TestInitGenesis(t *testing.T) {
	// mock the consumer genesis state values
	provClientID := "tendermint-07"
	provClientType := "07-tendermint"

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
				params,
			),
			func(ctx sdk.Context, ck consumerkeeper.Keeper, gs *consumertypes.GenesisState) {
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
			expectProviderClientExists(mocks)

			tc.malleate(ctx, mocks)

			consumerKeeper.InitGenesis(ctx, tc.genesis)

			tc.assertStates(ctx, consumerKeeper, tc.genesis)
		})
	}
}

func TestExportGenesis(t *testing.T) {
	provClientID := "tendermint-07"

	pubKey := ed25519.GenPrivKey().PubKey()
	tmPK, err := cryptocodec.ToCmtPubKeyInterface(pubKey)
	require.NoError(t, err)
	validator := tmtypes.NewValidator(tmPK, 1)
	valset := []abci.ValidatorUpdate{tmtypes.TM2PB.ValidatorUpdate(validator)}

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
			},
			consumertypes.NewRestartGenesisState(
				provClientID,
				valset,
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
	ck.SetLastVSCRecvTime(ctx, lastRecv)

	exported := ck.ExportGenesis(ctx)
	require.NotNil(t, exported.LastVscRecvTime, "export must carry last_vsc_recv_time")
	require.Equal(t, lastRecv, *exported.LastVscRecvTime)

	// Import half: a fresh keeper restores the exact time, not the block-time fallback.
	ck2, ctx2, ctrl2, mocks2 := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	expectProviderClientExists(mocks2)
	defer ctrl2.Finish()
	ck2.InitGenesis(ctx2, exported)
	require.Equal(t, lastRecv, ck2.GetLastVSCRecvTime(ctx2))
}

// TestGenesisRoundTripConsumerInDebt verifies the consumer's debt flag survives
// an export/import restart: it is the other arm of the tx-admission gate
// LastVSCRecvTime drives, and IsConsumerInDebt reads an unset flag as "not in
// debt", so a debt-gated consumer that did not carry the flag through genesis
// would come back admitting ordinary transactions until the next VSC packet
// re-asserted it.
func TestGenesisRoundTripConsumerInDebt(t *testing.T) {
	provClientID := "tendermint-07"
	params := vaastypes.DefaultConsumerParams()
	params.Enabled = true

	pubKey := ed25519.GenPrivKey().PubKey()
	tmPK, err := cryptocodec.ToCmtPubKeyInterface(pubKey)
	require.NoError(t, err)
	validator := tmtypes.NewValidator(tmPK, 1)

	// Export half: a consumer the provider has flagged as in debt.
	ck, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ck.SetParams(ctx, params)
	ck.SetProviderClientID(ctx, provClientID)
	cVal, err := consumertypes.NewCCValidator(validator.Address.Bytes(), 1, pubKey)
	require.NoError(t, err)
	ck.SetCCValidator(ctx, cVal)
	ck.SetConsumerInDebt(ctx, true)

	exported := ck.ExportGenesis(ctx)
	require.True(t, exported.ConsumerInDebt, "export must carry the debt flag")

	// Import half: a fresh keeper comes back gated.
	ck2, ctx2, ctrl2, mocks2 := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	expectProviderClientExists(mocks2)
	defer ctrl2.Finish()
	ck2.InitGenesis(ctx2, exported)
	require.True(t, ck2.IsConsumerInDebt(ctx2), "debt flag lost across the restart round-trip")

	reExported := ck2.ExportGenesis(ctx2)
	require.Equal(t, exported, reExported, "round-trip must be a fixed point")

	// A consumer that is not in debt exports a false flag and imports
	// identically to a fresh keeper.
	ck.SetConsumerInDebt(ctx, false)
	cleared := ck.ExportGenesis(ctx)
	require.False(t, cleared.ConsumerInDebt)

	ck3, ctx3, ctrl3, mocks3 := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	expectProviderClientExists(mocks3)
	defer ctrl3.Finish()
	ck3.InitGenesis(ctx3, cleared)
	require.False(t, ck3.IsConsumerInDebt(ctx3))
	require.Equal(t, cleared, ck3.ExportGenesis(ctx3), "round-trip must be a fixed point")
}

// TestInitGenesisNewChainPinsProviderChainId verifies that a brand-new consumer
// pins the provider chain id from the client state it is handed at genesis,
// rather than leaving no pin at all until the first VSC packet establishes one
// through authenticateProviderChainID.
func TestInitGenesisNewChainPinsProviderChainId(t *testing.T) {
	providerChainId := "provider-chain-1"
	provClientID := "07-tendermint-0"
	provClientType := "07-tendermint"

	cId := crypto.NewCryptoIdentityFromIntSeed(915237)
	validator := tmtypes.NewValidator(cId.TMCryptoPubKey(), 1)
	valset := []abci.ValidatorUpdate{tmtypes.TM2PB.ValidatorUpdate(validator)}

	provConsState := ibctmtypes.NewConsensusState(
		time.Unix(1_700_000_000, 0).UTC(),
		commitmenttypes.NewMerkleRoot([]byte("apphash")),
		tmtypes.NewValidatorSet([]*tmtypes.Validator{validator}).Hash(),
	)
	provClientState := ibctmtypes.NewClientState(
		providerChainId,
		ibctmtypes.DefaultTrustLevel,
		stakingtypes.DefaultUnbondingTime/2,
		stakingtypes.DefaultUnbondingTime,
		time.Second*10,
		// The revision number must match the chain id's, and the revision
		// height must be non-zero, for ClientState.Validate to accept it.
		clienttypes.NewHeight(1, 5),
		commitmenttypes.GetSDKSpecs(),
		[]string{"upgrade", "upgradedIBCState"},
	)

	params := vaastypes.DefaultConsumerParams()
	params.Enabled = true

	ck, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	clientStateBytes, err := provClientState.Marshal()
	require.NoError(t, err)
	consStateBytes, err := provConsState.Marshal()
	require.NoError(t, err)
	testkeeper.ExpectCreateClientMock(ctx, mocks, provClientType, provClientID, clientStateBytes, consStateBytes)

	genesis := consumertypes.NewInitialGenesisState(provClientState, provConsState, valset, params)
	require.NoError(t, genesis.Validate(), "test fixture must validate")
	ck.InitGenesis(ctx, genesis)

	got, ok := ck.GetProviderChainId(ctx)
	require.True(t, ok, "a new chain must pin the provider chain id at genesis")
	require.Equal(t, providerChainId, got,
		"the pin must come from the genesis client state, not from the first VSC packet")
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

	ck2, ctx2, ctrl2, mocks2 := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	expectProviderClientExists(mocks2)
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
	ck.SetProviderChainId(ctx, providerChainId)

	exported := ck.ExportGenesis(ctx)
	require.Equal(t, providerChainId, exported.ProviderChainId, "export must carry provider_chain_id")
	require.False(t, exported.NewChain, "restart export must not be a new-chain genesis")

	ck2, ctx2, ctrl2, mocks2 := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	expectProviderClientExists(mocks2)
	defer ctrl2.Finish()
	ck2.InitGenesis(ctx2, exported)

	gotChainId, ok := ck2.GetProviderChainId(ctx2)
	require.True(t, ok, "ProviderChainId lost across round-trip")
	require.Equal(t, providerChainId, gotChainId)

	reExported := ck2.ExportGenesis(ctx2)
	require.Equal(t, exported, reExported, "round-trip must be a fixed point")
}

// TestGenesisRoundTripHighestValsetUpdateID verifies the consumer's
// out-of-order dedup watermark (HighestValsetUpdateID) survives an
// export/import restart, and that once restored it keeps
// rejecting a stale diff VSC that arrives first after the restart, instead of
// applying an older set over a newer one. Before the fix InitGenesis left the
// watermark unset (found=false), so the dedup guard in OnRecvVSCPacketV2 was
// skipped and the stale diff would be applied.
func TestGenesisRoundTripHighestValsetUpdateID(t *testing.T) {
	provClientID := "07-tendermint-0"
	params := vaastypes.DefaultConsumerParams()
	params.Enabled = true

	pk1, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pk2, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	// Export half: a consumer that has applied VSC packets up to id 5.
	ck, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")
	ck.SetParams(ctx, params)
	// Pin the provider client the packets arrive over up front, as a real
	// consumer does at genesis.
	ck.SetProviderClientID(ctx, provClientID)

	applied := vaastypes.NewValidatorSetChangePacketData(
		[]abci.ValidatorUpdate{{PubKey: pk1, Power: 30}, {PubKey: pk2, Power: 20}}, 5)
	require.NoError(t, ck.OnRecvVSCPacketV2(ctx, provClientID, applied))

	highest, found, err := ck.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint64(5), highest)

	exported := ck.ExportGenesis(ctx)
	require.Equal(t, uint64(5), exported.HighestValsetUpdateId, "export must carry the dedup watermark")

	// Import half: a fresh keeper restores the watermark.
	ck2, ctx2, ctrl2, mocks2 := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl2.Finish()
	testkeeper.StubClientState(mocks2, "provider-0")
	ck2.InitGenesis(ctx2, exported)

	gotHighest, gotFound, err := ck2.GetHighestValsetUpdateID(ctx2)
	require.NoError(t, err)
	require.True(t, gotFound, "watermark lost across the restart round-trip")
	require.Equal(t, uint64(5), gotHighest)

	// A stale diff VSC (id 3 <= watermark 5), e.g. one still held in IBC state
	// across the restart, must be skipped -- it sets no pending changes.
	stale := vaastypes.NewValidatorSetChangePacketData(
		[]abci.ValidatorUpdate{{PubKey: pk1, Power: 999}}, 3)
	require.NoError(t, ck2.OnRecvVSCPacketV2(ctx2, provClientID, stale))
	_, hasPending := ck2.GetPendingChanges(ctx2)
	require.False(t, hasPending, "stale diff (id 3 <= watermark 5) must be rejected after restart")

	// A genuinely newer packet is still applied.
	newer := vaastypes.NewValidatorSetChangePacketData(
		[]abci.ValidatorUpdate{{PubKey: pk1, Power: 40}}, 6)
	require.NoError(t, ck2.OnRecvVSCPacketV2(ctx2, provClientID, newer))
	pending, ok := ck2.GetPendingChanges(ctx2)
	require.True(t, ok)
	require.NotEmpty(t, pending.ValidatorUpdates, "a newer VSC after restart must still be applied")
}

func assertProviderClientID(t *testing.T, ctx sdk.Context, ck *consumerkeeper.Keeper, clientID string) {
	t.Helper()
	cid, ok := ck.GetProviderClientID(ctx)
	require.True(t, ok)
	require.Equal(t, clientID, cid)
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

// TestInitGenesisPanicsOnInvalidStagedDowntimeParams verifies that InitGenesis
// halts on staged downtime params carrying a nil MinSignedPerWindow -- the
// value a genesis JSON that omits the min_signed_per_window key deserializes
// to -- instead of storing them for applyStagedDowntimeParams to copy into
// the consumer params at the next window close.
func TestInitGenesisPanicsOnInvalidStagedDowntimeParams(t *testing.T) {
	consumerKeeper, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	expectProviderClientExists(mocks)

	cId := crypto.NewCryptoIdentityFromIntSeed(738294)
	validator := tmtypes.NewValidator(cId.TMCryptoPubKey(), 1)
	valset := []abci.ValidatorUpdate{tmtypes.TM2PB.ValidatorUpdate(validator)}

	params := vaastypes.DefaultConsumerParams()
	params.Enabled = true
	genesis := consumertypes.NewRestartGenesisState(
		"07-tendermint-0",
		valset,
		params,
	)
	genesis.StagedDowntimeParams = &vaastypes.DowntimeParams{
		SignedBlocksWindow: params.SignedBlocksWindow,
		MinSignedPerWindow: math.LegacyDec{},
	}

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		consumerKeeper.InitGenesis(ctx, genesis)
	}()
	require.NotNil(t, recovered)
	require.Contains(t, fmt.Sprint(recovered), "min_signed_per_window")
}

// TestInitGenesisAcceptsDefaultGenesis verifies that InitGenesis accepts the
// module's own default genesis state.
func TestInitGenesisAcceptsDefaultGenesis(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	require.NotPanics(t, func() { consumerKeeper.InitGenesis(ctx, consumertypes.DefaultGenesisState()) })
}

// TestInitGenesisPanicsWhenPinnedClientMissing: a restart genesis naming a
// provider client that does not exist in the IBC client store means the VAAS
// and IBC genesis fragments came from different exports; InitChain must fail
// rather than pin a dead id that would silently reject every inbound packet.
func TestInitGenesisPanicsWhenPinnedClientMissing(t *testing.T) {
	ck, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	mocks.MockClientKeeper.EXPECT().GetClientState(gomock.Any(), "07-tendermint-9").Return(nil, false)

	cId := crypto.NewCryptoIdentityFromIntSeed(772934)
	validator := tmtypes.NewValidator(cId.TMCryptoPubKey(), 1)
	valset := []abci.ValidatorUpdate{tmtypes.TM2PB.ValidatorUpdate(validator)}
	params := vaastypes.DefaultConsumerParams()
	params.Enabled = true
	genesis := consumertypes.NewRestartGenesisState("07-tendermint-9", valset, params)

	require.PanicsWithError(t,
		`init: genesis pins provider client "07-tendermint-9", which does not exist in the IBC client store`,
		func() { ck.InitGenesis(ctx, genesis) })
}
