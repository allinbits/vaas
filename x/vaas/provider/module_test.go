package provider

import (
	"bytes"
	"testing"

	"cosmossdk.io/math"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	"github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// TestBeginBlockCommitsDebtStateWhenDistributionFails verifies that when
// DistributeConsumerFees errors, the debt flag is still set for underfunded
// consumers.
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

	consumerInDebtPool := k.GetConsumerFeePoolAddress(consumerInDebt)
	consumerPayingPool := k.GetConsumerFeePoolAddress(consumerPaying)

	// One bonded validator.
	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()
	opBytes := bytes.Repeat([]byte{0xfe}, 20)
	op, err := valAddrCodec.BytesToString(opBytes)
	require.NoError(t, err)
	pk := ed25519.GenPrivKey().PubKey()
	val, err := stakingtypes.NewValidator(op, pk, stakingtypes.Description{})
	require.NoError(t, err)
	val.Status = stakingtypes.Bonded
	val.Tokens = sdk.DefaultPowerReduction
	val.DelegatorShares = math.LegacyNewDecFromInt(sdk.DefaultPowerReduction)

	sharePerVal := feesPerEpoch.Amount.QuoRaw(1) // 1 bonded validator
	shareCoins := sdk.NewCoins(sdk.NewCoin("uphoton", sharePerVal))

	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val}, nil)

	// consumerInDebt: SendCoins fails with ErrInsufficientFunds
	mocks.MockBankKeeper.EXPECT().
		SendCoins(gomock.Any(), consumerInDebtPool, sdk.AccAddress(opBytes), shareCoins).
		Return(sdkerrors.ErrInsufficientFunds.Wrapf("spendable 5 < 6000"))

	// consumerPaying: SendCoins succeeds
	mocks.MockBankKeeper.EXPECT().
		SendCoins(gomock.Any(), consumerPayingPool, sdk.AccAddress(opBytes), shareCoins).
		Return(nil)

	require.NoError(t, appModule.BeginBlock(sdk.WrapSDKContext(ctx)))

	require.True(t, k.IsConsumerInDebt(ctx, consumerInDebt))
	require.False(t, k.IsConsumerInDebt(ctx, consumerPaying))
}

// TestBeginBlockSkipsFeeCollectionWhenNotAtEpochBoundary verifies that the
// per-epoch fee distribution only runs at epoch boundaries.
func TestBeginBlockSkipsFeeCollectionWhenNotAtEpochBoundary(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	appModule := NewAppModule(&k)

	consumer := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer, providertypes.CONSUMER_PHASE_LAUNCHED)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = feesPerBlock.Amount
	k.SetParams(ctx, providerParams)

	// Height 100 is not an epoch boundary (blocks_per_epoch=600)
	ctx = ctx.WithBlockHeight(100)

	// No bank or staking mock expectations — nothing should be called.
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
	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = feesPerBlock.Amount
	k.SetParams(ctx, providerParams)

	consumerFeePoolAddr := k.GetConsumerFeePoolAddress(consumer)

	// Height 600 is an epoch boundary (600 % 600 == 0)
	ctx = ctx.WithBlockHeight(600)

	// Expect bonded validators to be queried
	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()
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

	// Expect direct send from consumer pool to validator
	feesPerEpoch := sdk.NewCoin("uphoton", feesPerBlock.Amount.MulRaw(providertypes.DefaultBlocksPerEpoch))
	shareCoins := sdk.NewCoins(feesPerEpoch) // 1 validator → share = full epoch fee

	mocks.MockBankKeeper.EXPECT().
		SendCoins(gomock.Any(), consumerFeePoolAddr, sdk.AccAddress(opBytes), shareCoins).
		Return(nil)

	require.NoError(t, appModule.BeginBlock(sdk.WrapSDKContext(ctx)))
}
