package keeper_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"

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

	owner := "cosmos1exampleowner000000000000000000000000xyz"
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
