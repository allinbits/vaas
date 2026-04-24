package keeper_test

import (
	"bytes"
	"errors"
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

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = feesPerBlock
	k.SetParams(ctx, providerParams)
	consumer0FeePoolAddr := k.GetConsumerFeePoolAddress(consumer0)
	consumer1FeePoolAddr := k.GetConsumerFeePoolAddress(consumer1)

	gomock.InOrder(
		mocks.MockBankKeeper.EXPECT().
			GetBalance(gomock.Any(), consumer0FeePoolAddr, feesPerBlock.Denom).
			Return(feesPerBlock),
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer0FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerBlock)).
			Return(nil),
		mocks.MockBankKeeper.EXPECT().
			GetBalance(gomock.Any(), consumer1FeePoolAddr, feesPerBlock.Denom).
			Return(sdk.NewInt64Coin("uphoton", 25)),
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer1FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerBlock)).
			Return(nil),
	)

	total, err := k.CollectFeesFromConsumers(ctx)
	require.NoError(t, err)
	require.Equal(t, sdk.NewInt64Coin("uphoton", 20), total)
	require.False(t, k.IsConsumerInDebt(ctx, consumer0))
	require.False(t, k.IsConsumerInDebt(ctx, consumer1))
}

func TestCollectFeesFromConsumersSkipsWhenInsufficient(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	consumer1 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerPhase(ctx, consumer1, providertypes.CONSUMER_PHASE_LAUNCHED)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = feesPerBlock
	k.SetParams(ctx, providerParams)
	consumer0FeePoolAddr := k.GetConsumerFeePoolAddress(consumer0)
	consumer1FeePoolAddr := k.GetConsumerFeePoolAddress(consumer1)

	gomock.InOrder(
		mocks.MockBankKeeper.EXPECT().
			GetBalance(gomock.Any(), consumer0FeePoolAddr, feesPerBlock.Denom).
			Return(sdk.NewInt64Coin("uphoton", 5)),
		mocks.MockBankKeeper.EXPECT().
			GetBalance(gomock.Any(), consumer1FeePoolAddr, feesPerBlock.Denom).
			Return(sdk.NewInt64Coin("uphoton", 12)),
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer1FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerBlock)).
			Return(nil),
	)

	total, err := k.CollectFeesFromConsumers(ctx)
	require.NoError(t, err)
	require.Equal(t, sdk.NewInt64Coin("uphoton", 10), total)
	require.True(t, k.IsConsumerInDebt(ctx, consumer0))
	require.False(t, k.IsConsumerInDebt(ctx, consumer1))
}

func TestCollectFeesFromConsumersClearsDebtWhenRecovered(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerInDebt(ctx, consumer0, true)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = feesPerBlock
	k.SetParams(ctx, providerParams)
	consumer0FeePoolAddr := k.GetConsumerFeePoolAddress(consumer0)

	gomock.InOrder(
		mocks.MockBankKeeper.EXPECT().
			GetBalance(gomock.Any(), consumer0FeePoolAddr, feesPerBlock.Denom).
			Return(sdk.NewInt64Coin("uphoton", 25)),
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer0FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerBlock)).
			Return(nil),
	)

	total, err := k.CollectFeesFromConsumers(ctx)
	require.NoError(t, err)
	require.Equal(t, sdk.NewInt64Coin("uphoton", 10), total)
	require.False(t, k.IsConsumerInDebt(ctx, consumer0))
}

func TestCollectFeesFromConsumersContinuesWhenTransferFails(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	consumer1 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerPhase(ctx, consumer1, providertypes.CONSUMER_PHASE_LAUNCHED)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = feesPerBlock
	k.SetParams(ctx, providerParams)
	consumer0FeePoolAddr := k.GetConsumerFeePoolAddress(consumer0)
	consumer1FeePoolAddr := k.GetConsumerFeePoolAddress(consumer1)

	gomock.InOrder(
		mocks.MockBankKeeper.EXPECT().
			GetBalance(gomock.Any(), consumer0FeePoolAddr, feesPerBlock.Denom).
			Return(sdk.NewInt64Coin("uphoton", 15)),
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer0FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerBlock)).
			Return(errors.New("boom")),
		mocks.MockBankKeeper.EXPECT().
			GetBalance(gomock.Any(), consumer1FeePoolAddr, feesPerBlock.Denom).
			Return(sdk.NewInt64Coin("uphoton", 18)),
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer1FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerBlock)).
			Return(nil),
	)

	total, err := k.CollectFeesFromConsumers(ctx)
	require.NoError(t, err)
	require.Equal(t, sdk.NewInt64Coin("uphoton", 10), total)
	require.True(t, k.IsConsumerInDebt(ctx, consumer0))
	require.False(t, k.IsConsumerInDebt(ctx, consumer1))
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

	// Tokens differ intentionally to assert the split does NOT depend on stake.
	val1 := stakingtypes.Validator{
		OperatorAddress: valOp1,
		Tokens:          sdk.DefaultPowerReduction.MulRaw(10),
		DelegatorShares: math.LegacyNewDecFromInt(sdk.DefaultPowerReduction.MulRaw(10)),
		Status:          stakingtypes.Bonded,
	}
	val2 := stakingtypes.Validator{
		OperatorAddress: valOp2,
		Tokens:          sdk.DefaultPowerReduction.MulRaw(20),
		DelegatorShares: math.LegacyNewDecFromInt(sdk.DefaultPowerReduction.MulRaw(20)),
		Status:          stakingtypes.Bonded,
	}
	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = sdk.NewInt64Coin("uphoton", 10)
	k.SetParams(ctx, providerParams)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providerParams.FeesPerBlock.Denom).
		Return(sdk.NewInt64Coin("uphoton", 300))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)
	// Equal split: 300 / 2 = 150 each, regardless of stake.
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), providertypes.ModuleName, sdk.AccAddress(valAddr1), sdk.NewCoins(sdk.NewInt64Coin("uphoton", 150))).
		Return(nil)
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), providertypes.ModuleName, sdk.AccAddress(valAddr2), sdk.NewCoins(sdk.NewInt64Coin("uphoton", 150))).
		Return(nil)

	err = k.DistributeFeesToValidators(ctx)
	require.NoError(t, err)
}

func TestDistributeFeesToValidatorsZeroFees(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = sdk.NewInt64Coin("uphoton", 10)
	k.SetParams(ctx, providerParams)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providerParams.FeesPerBlock.Denom).
		Return(sdk.NewCoin("uphoton", math.ZeroInt()))

	err := k.DistributeFeesToValidators(ctx)
	require.NoError(t, err)
}

func TestDistributeFeesToValidatorsSkipsWhenFeesTooSmall(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	mkVal := func(op string) stakingtypes.Validator {
		return stakingtypes.Validator{
			OperatorAddress: op,
			Tokens:          sdk.DefaultPowerReduction,
			DelegatorShares: math.LegacyNewDecFromInt(sdk.DefaultPowerReduction),
			Status:          stakingtypes.Bonded,
		}
	}
	val1 := mkVal("cosmosvaloper1tiny000000000000000000000000000000000")
	val2 := mkVal("cosmosvaloper1tiny111111111111111111111111111111111")

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = sdk.NewInt64Coin("uphoton", 1)
	k.SetParams(ctx, providerParams)

	// Pool holds 1 coin, 2 validators → floor(1/2) = 0 per validator; leave pooled.
	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providerParams.FeesPerBlock.Denom).
		Return(sdk.NewInt64Coin("uphoton", 1))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)

	err := k.DistributeFeesToValidators(ctx)
	require.NoError(t, err)
}

// TestDistributeFeesToValidatorsRemainderStaysPooled checks that when the
// equal split does not divide evenly, each validator still gets an equal
// floor share and the remainder stays in the provider module account to be
// picked up by the next block.
func TestDistributeFeesToValidatorsRemainderStaysPooled(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	valAddr1 := bytes.Repeat([]byte{1}, 20)
	valAddr2 := bytes.Repeat([]byte{2}, 20)
	valAddr3 := bytes.Repeat([]byte{3}, 20)
	op1, err := valAddrCodec.BytesToString(valAddr1)
	require.NoError(t, err)
	op2, err := valAddrCodec.BytesToString(valAddr2)
	require.NoError(t, err)
	op3, err := valAddrCodec.BytesToString(valAddr3)
	require.NoError(t, err)

	mkVal := func(op string) stakingtypes.Validator {
		return stakingtypes.Validator{
			OperatorAddress: op,
			Tokens:          sdk.DefaultPowerReduction,
			DelegatorShares: math.LegacyNewDecFromInt(sdk.DefaultPowerReduction),
			Status:          stakingtypes.Bonded,
		}
	}

	// 10 coins, 3 validators → each gets floor(10/3) = 3, 1 coin stays pooled.
	val1 := mkVal(op1)
	val2 := mkVal(op2)
	val3 := mkVal(op3)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = sdk.NewInt64Coin("uphoton", 10)
	k.SetParams(ctx, providerParams)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providerParams.FeesPerBlock.Denom).
		Return(sdk.NewInt64Coin("uphoton", 10))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2, val3}, nil)
	expected := sdk.NewCoins(sdk.NewInt64Coin("uphoton", 3))
	for _, addr := range [][]byte{valAddr1, valAddr2, valAddr3} {
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromModuleToAccount(gomock.Any(), providertypes.ModuleName, sdk.AccAddress(addr), expected).
			Return(nil)
	}

	err = k.DistributeFeesToValidators(ctx)
	require.NoError(t, err)
}

func TestDistributeFeesToValidatorsNoValidators(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = sdk.NewInt64Coin("uphoton", 50)
	k.SetParams(ctx, providerParams)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providerParams.FeesPerBlock.Denom).
		Return(sdk.NewInt64Coin("uphoton", 50))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{}, nil)

	err := k.DistributeFeesToValidators(ctx)
	require.NoError(t, err)
}
