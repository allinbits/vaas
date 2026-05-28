package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
)

func TestQueryConsumerChainIncludesFeePoolAddress(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerChainId(ctx, consumerId, "consumer-chain")
	k.SetConsumerOwnerAddress(ctx, consumerId, "owner-address")
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_REGISTERED)
	require.NoError(t, k.SetConsumerMetadata(ctx, consumerId, providertypes.ConsumerMetadata{
		Name: "name", Description: "description", Metadata: "metadata",
	}))

	mocks.MockSlashingKeeper.EXPECT().SlashFractionDoubleSign(gomock.Any()).Return(math.LegacyNewDec(0), nil).AnyTimes()
	mocks.MockSlashingKeeper.EXPECT().SlashFractionDowntime(gomock.Any()).Return(math.LegacyNewDec(0), nil).AnyTimes()

	expected := k.GetConsumerFeePoolAddress(consumerId).String()

	res, err := k.QueryConsumerChain(ctx, &providertypes.QueryConsumerChainRequest{ConsumerId: consumerId})
	require.NoError(t, err)
	require.Equal(t, expected, res.FeePoolAddress)

	chain, err := k.GetConsumerChain(ctx, consumerId)
	require.NoError(t, err)
	require.Equal(t, expected, chain.FeePoolAddress)
}
