package keeper_test

import (
	"bytes"
	"fmt"
	"testing"

	"cosmossdk.io/math"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	"github.com/cosmos/cosmos-sdk/codec/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestCollectFeesFromConsumers(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	consumer1 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerPhase(ctx, consumer1, providertypes.CONSUMER_PHASE_LAUNCHED)

	feesPerBlock := sdk.NewInt64Coin("photon", 10)
	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = feesPerBlock
	k.SetParams(ctx, providerParams)

	gomock.InOrder(
		mocks.MockBankKeeper.EXPECT().
			GetBalance(gomock.Any(), authtypes.NewModuleAddress(fmt.Sprintf("consumer-%s", consumer0)), feesPerBlock.Denom).
			Return(feesPerBlock),
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromModuleToModule(gomock.Any(), fmt.Sprintf("consumer-%s", consumer0), authtypes.FeeCollectorName, sdk.NewCoins(feesPerBlock)).
			Return(nil),
		mocks.MockBankKeeper.EXPECT().
			GetBalance(gomock.Any(), authtypes.NewModuleAddress(fmt.Sprintf("consumer-%s", consumer1)), feesPerBlock.Denom).
			Return(sdk.NewInt64Coin("photon", 25)),
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromModuleToModule(gomock.Any(), fmt.Sprintf("consumer-%s", consumer1), authtypes.FeeCollectorName, sdk.NewCoins(feesPerBlock)).
			Return(nil),
	)

	total, err := k.CollectFeesFromConsumers(ctx)
	require.NoError(t, err)
	require.Equal(t, sdk.NewInt64Coin("photon", 20), total)
}

func TestCollectFeesFromConsumersSkipsWhenInsufficient(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	consumer1 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerPhase(ctx, consumer1, providertypes.CONSUMER_PHASE_LAUNCHED)

	feesPerBlock := sdk.NewInt64Coin("photon", 10)
	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = feesPerBlock
	k.SetParams(ctx, providerParams)

	gomock.InOrder(
		mocks.MockBankKeeper.EXPECT().
			GetBalance(gomock.Any(), authtypes.NewModuleAddress(fmt.Sprintf("consumer-%s", consumer0)), feesPerBlock.Denom).
			Return(sdk.NewInt64Coin("photon", 5)),
		mocks.MockBankKeeper.EXPECT().
			GetBalance(gomock.Any(), authtypes.NewModuleAddress(fmt.Sprintf("consumer-%s", consumer1)), feesPerBlock.Denom).
			Return(sdk.NewInt64Coin("photon", 12)),
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromModuleToModule(gomock.Any(), fmt.Sprintf("consumer-%s", consumer1), authtypes.FeeCollectorName, sdk.NewCoins(feesPerBlock)).
			Return(nil),
	)

	total, err := k.CollectFeesFromConsumers(ctx)
	require.NoError(t, err)
	require.Equal(t, sdk.NewInt64Coin("photon", 10), total)
}

func TestDistributeFeesToValidators(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	valAddr1 := bytes.Repeat([]byte{1}, 20)
	valAddr2 := bytes.Repeat([]byte{2}, 20)
	valOp1, err := valAddrCodec.BytesToString(valAddr1)
	require.NoError(t, err)
	valOp2, err := valAddrCodec.BytesToString(valAddr2)
	require.NoError(t, err)

	val1Tokens := sdk.DefaultPowerReduction.MulRaw(10)
	val2Tokens := sdk.DefaultPowerReduction.MulRaw(20)

	val1 := stakingtypes.Validator{
		OperatorAddress: valOp1,
		Tokens:          val1Tokens,
		DelegatorShares: math.LegacyNewDecFromInt(val1Tokens),
		Status:          stakingtypes.Bonded,
	}
	val2 := stakingtypes.Validator{
		OperatorAddress: valOp2,
		Tokens:          val2Tokens,
		DelegatorShares: math.LegacyNewDecFromInt(val2Tokens),
		Status:          stakingtypes.Bonded,
	}

	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)
	mocks.MockStakingKeeper.EXPECT().
		GetLastTotalPower(gomock.Any()).
		Return(math.NewInt(30), nil)
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), authtypes.FeeCollectorName, sdk.AccAddress(valAddr1), sdk.NewCoins(sdk.NewInt64Coin("photon", 100))).
		Return(nil)
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), authtypes.FeeCollectorName, sdk.AccAddress(valAddr2), sdk.NewCoins(sdk.NewInt64Coin("photon", 200))).
		Return(nil)

	err = k.DistributeFeesToValidators(ctx, sdk.NewInt64Coin("photon", 300))
	require.NoError(t, err)
}

func TestDistributeFeesToValidatorsNoValidators(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{}, nil)

	err := k.DistributeFeesToValidators(ctx, sdk.NewInt64Coin("photon", 50))
	require.NoError(t, err)
}
