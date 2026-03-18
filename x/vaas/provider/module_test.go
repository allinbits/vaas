package provider

import (
	"errors"
	"testing"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestBeginBlockCommitsDebtStateWhenDistributionFails(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	appModule := NewAppModule(&k)

	consumerInDebt := k.FetchAndIncrementConsumerId(ctx)
	consumerPaying := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumerInDebt, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerPhase(ctx, consumerPaying, providertypes.CONSUMER_PHASE_LAUNCHED)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = sdk.NewInt64Coin("photon", 10)
	k.SetParams(ctx, providerParams)

	consumerInDebtFeePoolAddr := k.GetConsumerFeePoolAddress(consumerInDebt)
	consumerPayingFeePoolAddr := k.GetConsumerFeePoolAddress(consumerPaying)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), consumerInDebtFeePoolAddr, providerParams.FeesPerBlock.Denom).
		Return(sdk.NewInt64Coin("photon", 5))
	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), consumerPayingFeePoolAddr, providerParams.FeesPerBlock.Denom).
		Return(sdk.NewInt64Coin("photon", 20))
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromAccountToModule(gomock.Any(), consumerPayingFeePoolAddr, authtypes.FeeCollectorName, sdk.NewCoins(providerParams.FeesPerBlock)).
		Return(nil)
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return(nil, errors.New("distribution boom"))

	err := appModule.BeginBlock(sdk.WrapSDKContext(ctx))
	require.NoError(t, err)

	require.True(t, k.IsConsumerInDebt(ctx, consumerInDebt))
	require.True(t, k.HasPendingConsumerDebtPacket(ctx, consumerInDebt))
	require.False(t, k.IsConsumerInDebt(ctx, consumerPaying))
	require.False(t, k.HasPendingConsumerDebtPacket(ctx, consumerPaying))
}
