package provider

import (
	"bytes"
	"testing"
	"time"

	"cosmossdk.io/math"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	"github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// TestBeginBlockCommitsDebtStateWhenDistributionFails verifies that when
// a consumer is underfunded it is marked in debt, while funded consumers
// pay normally.
func TestBeginBlockCommitsDebtStateWhenDistributionFails(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	appModule := NewAppModule(&k)

	consumerInDebt := k.FetchAndIncrementConsumerId(ctx)
	consumerPaying := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumerInDebt, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerPhase(ctx, consumerPaying, providertypes.CONSUMER_PHASE_LAUNCHED)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	feesPerEpoch := sdk.NewCoin("uphoton", feesPerBlock.Amount.MulRaw(providertypes.DefaultBlocksPerEpoch))
	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = feesPerBlock.Amount
	k.SetParams(ctx, providerParams)
	k.SetInfractionParams(ctx, providertypes.InfractionParameters{})

	consumerInDebtPool := k.GetConsumerFeePoolAddress(consumerInDebt)
	consumerPayingPool := k.GetConsumerFeePoolAddress(consumerPaying)

	// One bonded validator.
	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).AnyTimes()
	opBytes := bytes.Repeat([]byte{0xfe}, 20)
	op, err := valAddrCodec.BytesToString(opBytes)
	require.NoError(t, err)
	pk := ed25519.GenPrivKey().PubKey()
	val, err := stakingtypes.NewValidator(op, pk, stakingtypes.Description{})
	require.NoError(t, err)
	val.Status = stakingtypes.Bonded
	val.Tokens = sdk.DefaultPowerReduction
	val.DelegatorShares = math.LegacyNewDecFromInt(sdk.DefaultPowerReduction)

	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val}, nil)

	// consumerInDebt: balance too low -> in debt, no InputOutputCoins
	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), consumerInDebtPool, "uphoton").
		Return(sdk.NewCoin("uphoton", feesPerEpoch.Amount.QuoRaw(2)))

	// consumerPaying: balance sufficient → pays via InputOutputCoins
	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), consumerPayingPool, "uphoton").
		Return(feesPerEpoch)
	mocks.MockBankKeeper.EXPECT().
		InputOutputCoins(gomock.Any(),
			banktypes.Input{Address: consumerPayingPool.String(), Coins: sdk.NewCoins(feesPerEpoch)},
			[]banktypes.Output{
				{Address: sdk.AccAddress(opBytes).String(), Coins: sdk.NewCoins(feesPerEpoch)},
			},
		).Return(nil)

	require.NoError(t, appModule.BeginBlock(sdk.WrapSDKContext(ctx)))

	require.True(t, k.IsConsumerInDebt(ctx, consumerInDebt))
	require.False(t, k.IsConsumerInDebt(ctx, consumerPaying))
}

// TestBeginBlockSkipsFeeCollectionWhenNotAtEpochBoundary verifies that the
// per-epoch fee distribution only runs at epoch boundaries.
func TestBeginBlockSkipsFeeCollectionWhenNotAtEpochBoundary(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	appModule := NewAppModule(&k)

	consumer := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer, providertypes.CONSUMER_PHASE_LAUNCHED)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = feesPerBlock.Amount
	k.SetParams(ctx, providerParams)
	k.SetInfractionParams(ctx, providertypes.InfractionParameters{})

	// Height 100 is not an epoch boundary (blocks_per_epoch=600)
	ctx = ctx.WithBlockHeight(100)

	// SweepUnresponsiveConsumers calls UnbondingTime on every block.
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).AnyTimes()

	require.NoError(t, appModule.BeginBlock(sdk.WrapSDKContext(ctx)))
}

// TestBeginBlockCollectsFeesAtEpochBoundary verifies that fee distribution
// runs when the epoch boundary is reached.
func TestBeginBlockCollectsFeesAtEpochBoundary(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	appModule := NewAppModule(&k)

	consumer := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer, providertypes.CONSUMER_PHASE_LAUNCHED)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	feesPerEpoch := sdk.NewCoin("uphoton", feesPerBlock.Amount.MulRaw(providertypes.DefaultBlocksPerEpoch))
	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = feesPerBlock.Amount
	k.SetParams(ctx, providerParams)
	k.SetInfractionParams(ctx, providertypes.InfractionParameters{})

	consumerFeePoolAddr := k.GetConsumerFeePoolAddress(consumer)

	// Height 600 is an epoch boundary (600 % 600 == 0)
	ctx = ctx.WithBlockHeight(600)

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).AnyTimes()
	opBytes := bytes.Repeat([]byte{0xfe}, 20)
	op, err := valAddrCodec.BytesToString(opBytes)
	require.NoError(t, err)
	pk := ed25519.GenPrivKey().PubKey()
	val, err := stakingtypes.NewValidator(op, pk, stakingtypes.Description{})
	require.NoError(t, err)
	val.Status = stakingtypes.Bonded
	val.Tokens = sdk.DefaultPowerReduction
	val.DelegatorShares = math.LegacyNewDecFromInt(sdk.DefaultPowerReduction)

	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val}, nil)

	// Balance check passes, then single InputOutputCoins
	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), consumerFeePoolAddr, "uphoton").
		Return(feesPerEpoch)
	mocks.MockBankKeeper.EXPECT().
		InputOutputCoins(gomock.Any(),
			banktypes.Input{Address: consumerFeePoolAddr.String(), Coins: sdk.NewCoins(feesPerEpoch)},
			[]banktypes.Output{
				{Address: sdk.AccAddress(opBytes).String(), Coins: sdk.NewCoins(feesPerEpoch)},
			},
		).Return(nil)

	require.NoError(t, appModule.BeginBlock(sdk.WrapSDKContext(ctx)))
}
