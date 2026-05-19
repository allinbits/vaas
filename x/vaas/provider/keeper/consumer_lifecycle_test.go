package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"

	"github.com/stretchr/testify/require"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
)

func TestDeleteConsumerChain_RemovesReverseLookup(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	require.NoError(t, k.FeePoolAddressToConsumerId.Set(ctx, poolAddr, consumerId))
	k.SetConsumerClientId(ctx, consumerId, "07-tendermint-0")
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_STOPPED)

	require.NoError(t, k.DeleteConsumerChain(ctx, consumerId))

	_, err := k.FeePoolAddressToConsumerId.Get(ctx, poolAddr)
	require.ErrorIs(t, err, collections.ErrNotFound)
}
