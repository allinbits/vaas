package keeper_test

import (
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

func TestExportGenesis_ConsumerWithoutGenesis(t *testing.T) {
	pk, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	pk.SetParams(ctx, providertypes.DefaultParams())

	consumerId := pk.FetchAndIncrementConsumerId(ctx)
	pk.SetConsumerChainId(ctx, consumerId, "chain-"+consumerId)
	pk.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_REGISTERED)

	genState := pk.ExportGenesis(ctx)
	require.NotNil(t, genState)
	require.Len(t, genState.ConsumerStates, 1)
	require.Equal(t, "chain-"+consumerId, genState.ConsumerStates[0].ChainId)
	require.Equal(t, providertypes.CONSUMER_PHASE_REGISTERED, genState.ConsumerStates[0].Phase)
	require.Equal(t, "", genState.ConsumerStates[0].ClientId)
}

func TestExportGenesis_InitializedConsumerWithoutGenesis(t *testing.T) {
	pk, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	pk.SetParams(ctx, providertypes.DefaultParams())

	consumerId := pk.FetchAndIncrementConsumerId(ctx)
	pk.SetConsumerChainId(ctx, consumerId, "chain-"+consumerId)
	pk.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_INITIALIZED)

	genState := pk.ExportGenesis(ctx)
	require.NotNil(t, genState)
	require.Len(t, genState.ConsumerStates, 1)
	require.Equal(t, "chain-"+consumerId, genState.ConsumerStates[0].ChainId)
	require.Equal(t, providertypes.CONSUMER_PHASE_INITIALIZED, genState.ConsumerStates[0].Phase)
}

func TestExportGenesisIncludesNewFields(t *testing.T) {
	pk, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	owner := sdk.AccAddress([]byte("vaas-test-owner-1234")).String()
	metadata := providertypes.ConsumerMetadata{Name: "n", Description: "d", Metadata: "m"}
	initParams := providertypes.ConsumerInitializationParameters{
		InitialHeight:     clienttypes.Height{RevisionNumber: 0, RevisionHeight: 42},
		GenesisHash:       []byte("g"),
		BinaryHash:        []byte("b"),
		SpawnTime:         time.Unix(1_700_000_000, 0).UTC(),
		UnbondingPeriod:   time.Hour,
		VaasTimeoutPeriod: time.Hour,
		HistoricalEntries: 10,
	}
	removalTime := time.Unix(1_800_000_000, 0).UTC()
	cg := *vaastypes.DefaultConsumerGenesisState()
	cg.NewChain = true

	// Chain IDs use a non-numeric suffix so ParseChainID returns revision 0 for
	// all of them, letting a single initParams (RevisionNumber: 0) pass Validate().
	chainIDs := []string{"consumer-alpha", "consumer-beta", "consumer-gamma", "consumer-delta", "consumer-epsilon"}

	// Allocate five consumer ids ("0".."4") and set their chain ids.
	for i := range 5 {
		id := pk.FetchAndIncrementConsumerId(ctx)
		pk.SetConsumerChainId(ctx, id, chainIDs[i])
	}

	// id "0" REGISTERED.
	pk.SetConsumerPhase(ctx, "0", providertypes.CONSUMER_PHASE_REGISTERED)
	pk.SetConsumerOwnerAddress(ctx, "0", owner)
	require.NoError(t, pk.SetConsumerMetadata(ctx, "0", metadata))

	// id "1" INITIALIZED.
	pk.SetConsumerPhase(ctx, "1", providertypes.CONSUMER_PHASE_INITIALIZED)
	pk.SetConsumerOwnerAddress(ctx, "1", owner)
	require.NoError(t, pk.SetConsumerMetadata(ctx, "1", metadata))
	require.NoError(t, pk.SetConsumerInitializationParameters(ctx, "1", initParams))

	// id "2" LAUNCHED.
	pk.SetConsumerPhase(ctx, "2", providertypes.CONSUMER_PHASE_LAUNCHED)
	pk.SetConsumerOwnerAddress(ctx, "2", owner)
	require.NoError(t, pk.SetConsumerMetadata(ctx, "2", metadata))
	require.NoError(t, pk.SetConsumerInitializationParameters(ctx, "2", initParams))
	pk.SetConsumerClientId(ctx, "2", "07-tendermint-0")
	require.NoError(t, pk.SetConsumerGenesis(ctx, "2", cg))

	// id "3" STOPPED.
	pk.SetConsumerPhase(ctx, "3", providertypes.CONSUMER_PHASE_STOPPED)
	pk.SetConsumerOwnerAddress(ctx, "3", owner)
	require.NoError(t, pk.SetConsumerMetadata(ctx, "3", metadata))
	require.NoError(t, pk.SetConsumerInitializationParameters(ctx, "3", initParams))
	pk.SetConsumerClientId(ctx, "3", "07-tendermint-1")
	require.NoError(t, pk.SetConsumerGenesis(ctx, "3", cg))
	require.NoError(t, pk.SetConsumerRemovalTime(ctx, "3", removalTime))

	// id "4" DELETED.
	pk.SetConsumerPhase(ctx, "4", providertypes.CONSUMER_PHASE_DELETED)
	pk.SetConsumerOwnerAddress(ctx, "4", owner)
	require.NoError(t, pk.SetConsumerMetadata(ctx, "4", metadata))
	require.NoError(t, pk.SetConsumerInitializationParameters(ctx, "4", initParams))

	pk.SetParams(ctx, providertypes.DefaultParams())
	pk.SetValidatorSetUpdateId(ctx, 1)

	got := pk.ExportGenesis(ctx)
	require.NoError(t, got.Validate())

	require.Len(t, got.ConsumerStates, 5, "all five phases must be exported (including DELETED)")

	byID := map[string]providertypes.ConsumerState{}
	for _, cs := range got.ConsumerStates {
		byID[cs.ChainId] = cs
	}

	for _, id := range chainIDs {
		cs, ok := byID[id]
		require.True(t, ok, "consumer %s missing from export", id)
		require.Equal(t, owner, cs.OwnerAddress, "owner missing on consumer %s", id)
		require.NotNil(t, cs.Metadata, "metadata missing on consumer %s", id)
	}

	require.Nil(t, byID["consumer-alpha"].InitParams, "REGISTERED must not have init_params")
	require.NotNil(t, byID["consumer-beta"].InitParams, "INITIALIZED must have init_params")
	require.Equal(t, "07-tendermint-0", byID["consumer-gamma"].ClientId, "LAUNCHED must have client_id")
	require.NotNil(t, byID["consumer-delta"].RemovalTime, "STOPPED must carry removal_time")
	require.Equal(t, removalTime, *byID["consumer-delta"].RemovalTime)
	require.Equal(t, providertypes.CONSUMER_PHASE_DELETED, byID["consumer-epsilon"].Phase)
}

func TestInitGenesisRestoresPerConsumerStateAndDerivedQueues(t *testing.T) {
	pk, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	// InitGenesis calls InitGenesisValUpdates which queries the bonded validator set.
	// MaxValidators is not called (GetMaxProviderConsensusValidators reads from keeper params),
	// so we only expect GetBondedValidatorsByPower once.
	mocks.MockStakingKeeper.EXPECT().GetBondedValidatorsByPower(gomock.Any()).Return([]stakingtypes.Validator{}, nil).Times(1)

	owner := sdk.AccAddress([]byte("vaas-test-owner-1234")).String()
	md := providertypes.ConsumerMetadata{Name: "n", Description: "d", Metadata: "m"}
	spawnAt := time.Unix(1_700_000_000, 0).UTC()
	removeAt := time.Unix(1_800_000_000, 0).UTC()
	initialHeight := clienttypes.Height{RevisionNumber: 0, RevisionHeight: 42}
	ip := providertypes.ConsumerInitializationParameters{
		InitialHeight:     initialHeight,
		GenesisHash:       []byte("g"),
		BinaryHash:        []byte("b"),
		SpawnTime:         spawnAt,
		UnbondingPeriod:   time.Hour,
		VaasTimeoutPeriod: time.Hour,
		HistoricalEntries: 10,
	}
	cg := *vaastypes.DefaultConsumerGenesisState()
	cg.NewChain = true

	gs := &providertypes.GenesisState{
		ValsetUpdateId: 1,
		Params:         providertypes.DefaultParams(),
		ConsumerStates: []providertypes.ConsumerState{
			{ChainId: "consumer-alpha", Phase: providertypes.CONSUMER_PHASE_REGISTERED,
				OwnerAddress: owner, Metadata: &md},
			{ChainId: "consumer-beta", Phase: providertypes.CONSUMER_PHASE_INITIALIZED,
				OwnerAddress: owner, Metadata: &md, InitParams: &ip},
			{ChainId: "consumer-gamma", Phase: providertypes.CONSUMER_PHASE_LAUNCHED,
				OwnerAddress: owner, Metadata: &md, InitParams: &ip,
				ClientId: "07-tendermint-0", ConsumerGenesis: cg},
			{ChainId: "consumer-delta", Phase: providertypes.CONSUMER_PHASE_STOPPED,
				OwnerAddress: owner, Metadata: &md, InitParams: &ip,
				ClientId: "07-tendermint-1", ConsumerGenesis: cg, RemovalTime: &removeAt},
			{ChainId: "consumer-epsilon", Phase: providertypes.CONSUMER_PHASE_DELETED,
				OwnerAddress: owner, Metadata: &md, InitParams: &ip},
		},
	}

	require.NoError(t, gs.Validate(), "test fixture must validate")

	_ = pk.InitGenesis(ctx, gs)

	// Sanity check: the sequence counter must have advanced so that
	// GetAllConsumerIds returns all 5 imported consumers.
	allIds := pk.GetAllConsumerIds(ctx)
	require.Equal(t, []string{"0", "1", "2", "3", "4"}, allIds,
		"GetAllConsumerIds must return all imported consumer ids in order")

	// ConsumerStates are imported in order; InitGenesis allocates numeric ids
	// starting at "0":
	//   "0" → consumer-alpha  (REGISTERED)
	//   "1" → consumer-beta   (INITIALIZED)
	//   "2" → consumer-gamma  (LAUNCHED)
	//   "3" → consumer-delta  (STOPPED)
	//   "4" → consumer-epsilon (DELETED)
	idChain := []struct{ consumerId, chainId string }{
		{"0", "consumer-alpha"},
		{"1", "consumer-beta"},
		{"2", "consumer-gamma"},
		{"3", "consumer-delta"},
		{"4", "consumer-epsilon"},
	}

	// Per-consumer fields: chain id, owner, metadata must be present on all five.
	for _, entry := range idChain {
		gotChain, err := pk.GetConsumerChainId(ctx, entry.consumerId)
		require.NoError(t, err, "chain id missing for consumer %s", entry.consumerId)
		require.Equal(t, entry.chainId, gotChain)

		gotOwner, err := pk.GetConsumerOwnerAddress(ctx, entry.consumerId)
		require.NoError(t, err, "owner missing for consumer %s", entry.consumerId)
		require.Equal(t, owner, gotOwner)

		gotMd, err := pk.GetConsumerMetadata(ctx, entry.consumerId)
		require.NoError(t, err, "metadata missing for consumer %s", entry.consumerId)
		require.Equal(t, md, gotMd)
	}

	// init_params are set on INITIALIZED, LAUNCHED, STOPPED, DELETED (ids 1–4).
	for _, consumerId := range []string{"1", "2", "3", "4"} {
		gotIp, err := pk.GetConsumerInitializationParameters(ctx, consumerId)
		require.NoError(t, err, "init_params missing for consumer %s", consumerId)
		require.Equal(t, ip, gotIp)
	}

	// Spawn queue: only INITIALIZED (id "1") is enqueued at init_params.spawn_time.
	spawnIds, err := pk.GetConsumersToBeLaunched(ctx, spawnAt)
	require.NoError(t, err)
	require.Equal(t, []string{"1"}, spawnIds.Ids)

	// Removal queue: only STOPPED (id "3") is enqueued at removal_time.
	removeIds, err := pk.GetConsumersToBeRemoved(ctx, removeAt)
	require.NoError(t, err)
	require.Equal(t, []string{"3"}, removeIds.Ids)

	// EquivocationEvidenceMinHeight: set for LAUNCHED ("2") and STOPPED ("3") only.
	require.Equal(t, initialHeight.RevisionHeight, pk.GetEquivocationEvidenceMinHeight(ctx, "2"))
	require.Equal(t, initialHeight.RevisionHeight, pk.GetEquivocationEvidenceMinHeight(ctx, "3"))
	require.Zero(t, pk.GetEquivocationEvidenceMinHeight(ctx, "0"))
	require.Zero(t, pk.GetEquivocationEvidenceMinHeight(ctx, "1"))
	require.Zero(t, pk.GetEquivocationEvidenceMinHeight(ctx, "4"))

	// RemovalTime: per-consumer collection set for STOPPED (id "3").
	rt, err := pk.GetConsumerRemovalTime(ctx, "3")
	require.NoError(t, err)
	require.Equal(t, removeAt, rt)
}

// TestGenesisRoundTrip verifies that ExportGenesis -> Validate -> fresh
// keeper -> InitGenesis -> ExportGenesis produces an equal GenesisState
// across all five consumer phases.
func TestGenesisRoundTrip(t *testing.T) {
	pkA, ctxA, ctrlA, stakingA := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrlA.Finish()

	owner := sdk.AccAddress([]byte("vaas-test-owner-1234")).String()
	md := providertypes.ConsumerMetadata{Name: "n", Description: "d", Metadata: "m"}
	spawnAt := time.Unix(1_700_000_000, 0).UTC()
	removeAt := time.Unix(1_800_000_000, 0).UTC()
	ip := providertypes.ConsumerInitializationParameters{
		InitialHeight:     clienttypes.Height{RevisionNumber: 0, RevisionHeight: 42},
		GenesisHash:       []byte("g"),
		BinaryHash:        []byte("b"),
		SpawnTime:         spawnAt,
		UnbondingPeriod:   time.Hour,
		VaasTimeoutPeriod: time.Hour,
		HistoricalEntries: 10,
	}
	cg := *vaastypes.DefaultConsumerGenesisState()
	cg.NewChain = true

	// Seed keeper A by going through FetchAndIncrementConsumerId for each
	// consumer, mirroring what production code does at MsgCreateConsumer time.
	// This produces consumer ids "0".."4" in alpha..epsilon order.
	type seed struct {
		chainId  string
		phase    providertypes.ConsumerPhase
		clientId string
		setRT    bool
		setSpawn bool // → enqueue spawn
		setRem   bool // → enqueue removal + setMinHeight
		setMinHt bool // → setEquivocationEvidenceMinHeight (LAUNCHED+STOPPED)
	}
	seeds := []seed{
		{"consumer-alpha", providertypes.CONSUMER_PHASE_REGISTERED, "", false, false, false, false},
		{"consumer-beta", providertypes.CONSUMER_PHASE_INITIALIZED, "", false, true, false, false},
		{"consumer-gamma", providertypes.CONSUMER_PHASE_LAUNCHED, "07-tendermint-0", false, false, false, true},
		{"consumer-delta", providertypes.CONSUMER_PHASE_STOPPED, "07-tendermint-1", true, false, true, true},
		{"consumer-epsilon", providertypes.CONSUMER_PHASE_DELETED, "", false, false, false, false},
	}
	for _, s := range seeds {
		id := pkA.FetchAndIncrementConsumerId(ctxA)
		pkA.SetConsumerChainId(ctxA, id, s.chainId)
		pkA.SetConsumerPhase(ctxA, id, s.phase)
		pkA.SetConsumerOwnerAddress(ctxA, id, owner)
		require.NoError(t, pkA.SetConsumerMetadata(ctxA, id, md))
		// init_params present for INITIALIZED..DELETED (every phase except REGISTERED).
		if s.phase != providertypes.CONSUMER_PHASE_REGISTERED {
			require.NoError(t, pkA.SetConsumerInitializationParameters(ctxA, id, ip))
		}
		if s.clientId != "" {
			pkA.SetConsumerClientId(ctxA, id, s.clientId)
		}
		if s.phase == providertypes.CONSUMER_PHASE_LAUNCHED || s.phase == providertypes.CONSUMER_PHASE_STOPPED {
			require.NoError(t, pkA.SetConsumerGenesis(ctxA, id, cg))
		}
		if s.setRT {
			require.NoError(t, pkA.SetConsumerRemovalTime(ctxA, id, removeAt))
		}
		if s.setSpawn {
			require.NoError(t, pkA.AppendConsumerToBeLaunched(ctxA, id, spawnAt))
		}
		if s.setRem {
			require.NoError(t, pkA.AppendConsumerToBeRemoved(ctxA, id, removeAt))
		}
		if s.setMinHt {
			pkA.SetEquivocationEvidenceMinHeight(ctxA, id, ip.InitialHeight.RevisionHeight)
		}
	}
	pkA.SetParams(ctxA, providertypes.DefaultParams())
	pkA.SetValidatorSetUpdateId(ctxA, 1)

	expA := pkA.ExportGenesis(ctxA)
	require.NoError(t, expA.Validate(), "first export must validate")

	// Fresh keeper B.
	pkB, ctxB, ctrlB, stakingB := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrlB.Finish()

	// InitGenesisValUpdates reads from staking; mock it on both keepers.
	stakingA.MockStakingKeeper.EXPECT().GetBondedValidatorsByPower(gomock.Any()).Return(nil, nil).AnyTimes()
	stakingB.MockStakingKeeper.EXPECT().GetBondedValidatorsByPower(gomock.Any()).Return(nil, nil).AnyTimes()

	_ = pkB.InitGenesis(ctxB, expA)
	expB := pkB.ExportGenesis(ctxB)

	// Order-independent comparison on ConsumerStates (the only slice whose
	// order is not part of the contract).
	sort.Slice(expA.ConsumerStates, func(i, j int) bool {
		return expA.ConsumerStates[i].ChainId < expA.ConsumerStates[j].ChainId
	})
	sort.Slice(expB.ConsumerStates, func(i, j int) bool {
		return expB.ConsumerStates[i].ChainId < expB.ConsumerStates[j].ChainId
	})

	require.Equal(t, expA, expB, "round-trip must be a fixed point")
}
