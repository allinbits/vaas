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
	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = sdk.NewInt64Coin("uphoton", 10)
	k.SetParams(ctx, providerParams)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providerParams.FeesPerBlock.Denom).
		Return(sdk.NewInt64Coin("uphoton", 300))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)
	mocks.MockStakingKeeper.EXPECT().
		GetLastTotalPower(gomock.Any()).
		Return(math.NewInt(30), nil)
	mocks.MockStakingKeeper.EXPECT().
		PowerReduction(gomock.Any()).
		Return(sdk.DefaultPowerReduction).
		AnyTimes()
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), providertypes.ModuleName, sdk.AccAddress(valAddr1), sdk.NewCoins(sdk.NewInt64Coin("uphoton", 100))).
		Return(nil)
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), providertypes.ModuleName, sdk.AccAddress(valAddr2), sdk.NewCoins(sdk.NewInt64Coin("uphoton", 200))).
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

	val1Tokens := sdk.DefaultPowerReduction.MulRaw(10)
	val2Tokens := sdk.DefaultPowerReduction.MulRaw(10)

	val1 := stakingtypes.Validator{
		OperatorAddress: "cosmosvaloper1tiny000000000000000000000000000000000",
		Tokens:          val1Tokens,
		DelegatorShares: math.LegacyNewDecFromInt(val1Tokens),
		Status:          stakingtypes.Bonded,
	}
	val2 := stakingtypes.Validator{
		OperatorAddress: "cosmosvaloper1tiny111111111111111111111111111111111",
		Tokens:          val2Tokens,
		DelegatorShares: math.LegacyNewDecFromInt(val2Tokens),
		Status:          stakingtypes.Bonded,
	}
	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = sdk.NewInt64Coin("uphoton", 1)
	k.SetParams(ctx, providerParams)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providerParams.FeesPerBlock.Denom).
		Return(sdk.NewInt64Coin("uphoton", 1))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)
	mocks.MockStakingKeeper.EXPECT().
		GetLastTotalPower(gomock.Any()).
		Return(math.NewInt(20), nil)
	mocks.MockStakingKeeper.EXPECT().
		PowerReduction(gomock.Any()).
		Return(sdk.DefaultPowerReduction)

	err := k.DistributeFeesToValidators(ctx)
	require.NoError(t, err)
}

// TestDistributeFeesToValidatorsRemainderStaysPooled checks that when
// proportional shares don't divide evenly, only the floor shares are sent
// and the integer-division remainder stays in the provider module account
// to be picked up by the next block.
func TestDistributeFeesToValidatorsRemainderStaysPooled(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	valAddrTop := bytes.Repeat([]byte{1}, 20)
	valAddrMid := bytes.Repeat([]byte{2}, 20)
	valAddrTail := bytes.Repeat([]byte{3}, 20)
	opTop, err := valAddrCodec.BytesToString(valAddrTop)
	require.NoError(t, err)
	opMid, err := valAddrCodec.BytesToString(valAddrMid)
	require.NoError(t, err)
	opTail, err := valAddrCodec.BytesToString(valAddrTail)
	require.NoError(t, err)

	mkVal := func(op string, powerMul int64) stakingtypes.Validator {
		tokens := sdk.DefaultPowerReduction.MulRaw(powerMul)
		return stakingtypes.Validator{
			OperatorAddress: op,
			Tokens:          tokens,
			DelegatorShares: math.LegacyNewDecFromInt(tokens),
			Status:          stakingtypes.Bonded,
		}
	}

	// Powers 97 / 1 / 1, totalFees 10, totalPower 99. Floor shares are 9/0/0;
	// only the top validator's 9 coins are sent — the remaining 1 coin stays
	// pooled in the provider module account.
	valTop := mkVal(opTop, 97)
	valMid := mkVal(opMid, 1)
	valTail := mkVal(opTail, 1)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = sdk.NewInt64Coin("uphoton", 10)
	k.SetParams(ctx, providerParams)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providerParams.FeesPerBlock.Denom).
		Return(sdk.NewInt64Coin("uphoton", 10))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{valTop, valMid, valTail}, nil)
	mocks.MockStakingKeeper.EXPECT().
		GetLastTotalPower(gomock.Any()).
		Return(math.NewInt(99), nil)
	mocks.MockStakingKeeper.EXPECT().
		PowerReduction(gomock.Any()).
		Return(sdk.DefaultPowerReduction).
		AnyTimes()
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), providertypes.ModuleName, sdk.AccAddress(valAddrTop), sdk.NewCoins(sdk.NewInt64Coin("uphoton", 9))).
		Return(nil)

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
