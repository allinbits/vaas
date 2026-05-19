package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
)

func TestFeePoolSendRestriction(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := "0"
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	providerAddr := authtypes.NewModuleAddress(providertypes.ModuleName)
	user := sdk.AccAddress([]byte("user____________"))
	other := sdk.AccAddress([]byte("other___________"))

	require.NoError(t, k.FeePoolAddressToConsumerId.Set(ctx, poolAddr, consumerId))

	restrict := k.FeePoolSendRestriction()
	amt := sdk.NewCoins(sdk.NewInt64Coin("uphoton", 1))

	// unrelated destination passes through
	to, err := restrict(ctx, user, other, amt)
	require.NoError(t, err)
	require.Equal(t, other, to)

	// direct send to fee pool blocked
	_, err = restrict(ctx, user, poolAddr, amt)
	require.ErrorIs(t, err, providertypes.ErrUnsolicitedFeePoolDeposit)

	// sanctioned 2-hop send from provider module allowed
	to, err = restrict(ctx, providerAddr, poolAddr, amt)
	require.NoError(t, err)
	require.Equal(t, poolAddr, to)
}
