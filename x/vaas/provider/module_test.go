package provider

import (
	"bytes"
	"errors"
	"testing"

	"cosmossdk.io/math"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	"github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// TestBeginBlockCommitsDebtStateWhenDistributionFails verifies that when the
// distribution step errors, the fee-collection state (including the
// consumer-in-debt flag) is still committed — distribution runs in its own
// CacheContext so its rollback does not undo collection.
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

	consumerInDebtFeePoolAddr := k.GetConsumerFeePoolAddress(consumerInDebt)
	consumerPayingFeePoolAddr := k.GetConsumerFeePoolAddress(consumerPaying)

	// Prime one bonded validator so distribution attempts to pay out.
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

	// Collection phase: one consumer underfunded (ErrInsufficientFunds), one pays.
	// Fees are charged per epoch: fees_per_block * blocks_per_epoch.
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromAccountToModule(gomock.Any(), consumerInDebtFeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerEpoch)).
		Return(sdkerrors.ErrInsufficientFunds.Wrapf("spendable 5 < 6000"))
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromAccountToModule(gomock.Any(), consumerPayingFeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerEpoch)).
		Return(nil)
	// Distribution phase: bank send errors out, rolls back distribution only.
	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providertypes.DefaultFeesPerBlockDenom).
		Return(feesPerEpoch)
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val}, nil)
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), providertypes.ModuleName, sdk.AccAddress(opBytes), sdk.NewCoins(feesPerEpoch)).
		Return(errors.New("distribution boom"))

	require.NoError(t, appModule.BeginBlock(sdk.WrapSDKContext(ctx)))

	require.True(t, k.IsConsumerInDebt(ctx, consumerInDebt))
	require.False(t, k.IsConsumerInDebt(ctx, consumerPaying))
}

// TestBeginBlockSkipsFeeCollectionWhenNotAtEpochBoundary verifies that the
// per-epoch fee collection and distribution only run at epoch boundaries.
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

	// Set height to 100 (not an epoch boundary for blocks_per_epoch=600)
	ctx = ctx.WithBlockHeight(100)

	// No bank or staking mock expectations — nothing should be called.
	require.NoError(t, appModule.BeginBlock(sdk.WrapSDKContext(ctx)))
}

// TestBeginBlockCollectsFeesAtEpochBoundary verifies that fee collection
// and distribution run when the epoch boundary is reached.
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

	consumerFeePoolAddr := k.GetConsumerFeePoolAddress(consumer)

	// Height 600 is an epoch boundary (600 % 600 == 0)
	ctx = ctx.WithBlockHeight(600)

	// Expect per-epoch collection and zero-fee distribution
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromAccountToModule(gomock.Any(), consumerFeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerEpoch)).
		Return(nil)
	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providertypes.DefaultFeesPerBlockDenom).
		Return(sdk.NewCoin("uphoton", math.ZeroInt()))

	require.NoError(t, appModule.BeginBlock(sdk.WrapSDKContext(ctx)))
}
