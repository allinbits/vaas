package keeper_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	storetypes "cosmossdk.io/store/types"

	sdk "github.com/cosmos/cosmos-sdk/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// TestLiveConsumerIdsFollowTheLifecycle: a consumer joins the live set when
// it is created, stays in it through initialization, launch, pause and stop,
// and leaves it when it is deleted; the full id range keeps the deleted id
// for the export.
func TestLiveConsumerIdsFollowTheLifecycle(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).AnyTimes()

	require.Empty(t, k.GetLiveConsumerIds(ctx))

	msgSrv := providerkeeper.NewMsgServerImpl(&k)
	resp, err := msgSrv.CreateConsumer(ctx, &providertypes.MsgCreateConsumer{
		Submitter: validSubmitter(),
		ChainId:   "chain-0",
		Metadata:  providertypes.ConsumerMetadata{Name: "chain name"},
		InitializationParameters: &providertypes.ConsumerInitializationParameters{
			UnbondingPeriod: 21 * 24 * time.Hour,
		},
	})
	require.NoError(t, err)
	consumerId := resp.ConsumerId
	require.Equal(t, []uint64{consumerId}, k.GetLiveConsumerIds(ctx))

	for _, phase := range []providertypes.ConsumerPhase{
		providertypes.CONSUMER_PHASE_INITIALIZED,
		providertypes.CONSUMER_PHASE_LAUNCHED,
		providertypes.CONSUMER_PHASE_PAUSED,
		providertypes.CONSUMER_PHASE_LAUNCHED,
		providertypes.CONSUMER_PHASE_STOPPED,
	} {
		k.SetConsumerPhase(ctx, consumerId, phase)
		require.Equal(t, []uint64{consumerId}, k.GetLiveConsumerIds(ctx), "phase %s", phase)
	}

	// The deletion sweeps the fee pool, which reads the pool's balance.
	mocks.MockBankKeeper.EXPECT().GetAllBalances(ctx, k.GetConsumerFeePoolAddress(consumerId)).Return(sdk.NewCoins())
	require.NoError(t, k.DeleteConsumerChain(ctx, consumerId))

	require.Equal(t, providertypes.CONSUMER_PHASE_DELETED, k.GetConsumerPhase(ctx, consumerId))
	require.Empty(t, k.GetLiveConsumerIds(ctx))
	require.Equal(t, []uint64{consumerId}, k.GetAllConsumerIds(ctx))
}

// TestLiveConsumerWalksCostNothingForDeletedConsumers: the per-block and
// per-epoch walks and the key-assignment collision checks visit only the
// live consumers. A thousand deleted consumers created between two launched
// ones add no store reads: the walks consume exactly the gas they consume on
// a provider that only ever created the two launched consumers.
func TestLiveConsumerWalksCostNothingForDeletedConsumers(t *testing.T) {
	consAddr := sdk.ConsAddress([]byte("consAddr____________"))
	providerAddr := providertypes.NewProviderConsAddress([]byte("providerAddr________"))

	walks := []struct {
		name string
		walk func(k providerkeeper.Keeper, ctx sdk.Context)
	}{
		{"GetAllLaunchedConsumerIds", func(k providerkeeper.Keeper, ctx sdk.Context) { k.GetAllLaunchedConsumerIds(ctx) }},
		{"GetAllActiveConsumerIds", func(k providerkeeper.Keeper, ctx sdk.Context) { k.GetAllActiveConsumerIds(ctx) }},
		{"IsConsumerConsAddrInUse", func(k providerkeeper.Keeper, ctx sdk.Context) { k.IsConsumerConsAddrInUse(ctx, consAddr) }},
		{"IsConsumerConsAddrHeldByAnother", func(k providerkeeper.Keeper, ctx sdk.Context) {
			k.IsConsumerConsAddrHeldByAnother(ctx, consAddr, providerAddr)
		}},
		{"EndBlockPruneKeyAssignments", func(k providerkeeper.Keeper, ctx sdk.Context) { k.EndBlockPruneKeyAssignments(ctx) }},
	}

	// seed creates a launched consumer, then deleted ones, then another
	// launched consumer, so the deleted ids sit inside the id range.
	seed := func(t *testing.T, deleted int) (providerkeeper.Keeper, sdk.Context) {
		t.Helper()
		k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
		t.Cleanup(ctrl.Finish)
		launch := func() {
			id := k.FetchAndIncrementConsumerId(ctx)
			k.SetConsumerPhase(ctx, id, providertypes.CONSUMER_PHASE_LAUNCHED)
		}
		launch()
		for i := 0; i < deleted; i++ {
			id := k.FetchAndIncrementConsumerId(ctx)
			k.SetConsumerPhase(ctx, id, providertypes.CONSUMER_PHASE_REGISTERED)
			k.SetConsumerPhase(ctx, id, providertypes.CONSUMER_PHASE_DELETED)
		}
		launch()
		return k, ctx
	}
	gasOf := func(k providerkeeper.Keeper, ctx sdk.Context, walk func(providerkeeper.Keeper, sdk.Context)) storetypes.Gas {
		ctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		walk(k, ctx)
		return ctx.GasMeter().GasConsumed()
	}

	for _, w := range walks {
		t.Run(w.name, func(t *testing.T) {
			sparse, sparseCtx := seed(t, 0)
			dense, denseCtx := seed(t, 1000)
			require.Len(t, dense.GetAllConsumerIds(denseCtx), 1002)
			require.Equal(t, []uint64{0, 1001}, dense.GetLiveConsumerIds(denseCtx))
			require.Equal(t, []uint64{0, 1001}, dense.GetAllLaunchedConsumerIds(denseCtx))

			sparseGas := gasOf(sparse, sparseCtx, w.walk)
			require.Positive(t, sparseGas)
			require.Equal(t, sparseGas, gasOf(dense, denseCtx, w.walk))
		})
	}
}

// TestInitGenesisRebuildsLiveConsumerIds: the live set is not exported. The
// import derives it from the consumer states whose phase is not DELETED, and
// the export still carries every consumer, deleted ones included.
func TestInitGenesisRebuildsLiveConsumerIds(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	// InitGenesisValUpdates queries the staking keeper for the validator set.
	mocks.MockStakingKeeper.EXPECT().GetBondedValidatorsByPower(gomock.Any()).Return(nil, nil).Times(1)
	// InitGenesis walks fee-pool addresses for an orphan-balance check.
	mocks.MockBankKeeper.EXPECT().GetAllBalances(gomock.Any(), gomock.Any()).
		Return(sdk.NewCoins()).AnyTimes()

	gs := providertypes.DefaultGenesisState()
	gs.ConsumerStates = []providertypes.ConsumerState{
		{
			ConsumerId:      0,
			ChainId:         "chain-a",
			Phase:           providertypes.CONSUMER_PHASE_LAUNCHED,
			ConsumerGenesis: launchedConsumerGenesis(),
		},
		{
			ConsumerId:      1,
			Phase:           providertypes.CONSUMER_PHASE_DELETED,
			ConsumerGenesis: *vaastypes.DefaultConsumerGenesisState(),
		},
		{
			ConsumerId:      2,
			ChainId:         "chain-c",
			Phase:           providertypes.CONSUMER_PHASE_REGISTERED,
			ConsumerGenesis: *vaastypes.DefaultConsumerGenesisState(),
		},
	}

	k.InitGenesis(ctx, gs)

	require.Equal(t, []uint64{0, 2}, k.GetLiveConsumerIds(ctx))
	require.Equal(t, []uint64{0}, k.GetAllLaunchedConsumerIds(ctx))

	exported := k.ExportGenesis(ctx)
	exportedIds := make([]uint64, 0, len(exported.ConsumerStates))
	for _, cs := range exported.ConsumerStates {
		exportedIds = append(exportedIds, cs.ConsumerId)
	}
	require.Equal(t, []uint64{0, 1, 2}, exportedIds)
}
