package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
)

func TestExportGenesis_ConsumerWithoutGenesis(t *testing.T) {
	pk, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	pk.SetParams(ctx, providertypes.DefaultParams())

	consumerId := pk.FetchAndIncrementConsumerId(ctx)
	pk.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_REGISTERED)

	genState := pk.ExportGenesis(ctx)
	require.NotNil(t, genState)
	require.Len(t, genState.ConsumerStates, 1)
	require.Equal(t, consumerId, genState.ConsumerStates[0].ChainId)
	require.Equal(t, providertypes.CONSUMER_PHASE_REGISTERED, genState.ConsumerStates[0].Phase)
	require.Equal(t, "", genState.ConsumerStates[0].ClientId)
}

func TestExportGenesis_InitializedConsumerWithoutGenesis(t *testing.T) {
	pk, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	pk.SetParams(ctx, providertypes.DefaultParams())

	consumerId := pk.FetchAndIncrementConsumerId(ctx)
	pk.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_INITIALIZED)

	genState := pk.ExportGenesis(ctx)
	require.NotNil(t, genState)
	require.Len(t, genState.ConsumerStates, 1)
	require.Equal(t, consumerId, genState.ConsumerStates[0].ChainId)
	require.Equal(t, providertypes.CONSUMER_PHASE_INITIALIZED, genState.ConsumerStates[0].Phase)
}

func TestExportGenesis_LaunchedConsumerWithoutGenesisPanics(t *testing.T) {
	pk, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	pk.SetParams(ctx, providertypes.DefaultParams())

	consumerId := pk.FetchAndIncrementConsumerId(ctx)
	pk.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)

	require.Panics(t, func() {
		pk.ExportGenesis(ctx)
	})
}
