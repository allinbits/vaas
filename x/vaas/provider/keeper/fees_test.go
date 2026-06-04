package keeper_test

import (
	"bytes"
	"errors"
	"testing"

	addresscodec "cosmossdk.io/core/address"
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
			SendCoinsFromAccountToModule(gomock.Any(), consumer0FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerBlock)).
			Return(nil),
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer1FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerBlock)).
			Return(nil),
	)

	total := k.CollectFeesFromConsumers(ctx)
	require.Equal(t, sdk.NewInt64Coin("uphoton", 20), total)
	require.False(t, k.IsConsumerInDebt(ctx, consumer0))
	require.False(t, k.IsConsumerInDebt(ctx, consumer1))
}

// TestCollectFeesFromConsumers_PerConsumerOverride verifies that each consumer
// is charged its effective per-block fee: consumer1 has an override and pays
// the override amount; consumer0 has no override and pays Params.FeesPerBlock.
func TestCollectFeesFromConsumers_PerConsumerOverride(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	consumer1 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerPhase(ctx, consumer1, providertypes.CONSUMER_PHASE_LAUNCHED)

	defaultFees := sdk.NewInt64Coin("uphoton", 10)
	overrideAmount := math.NewInt(25)
	overrideFees := sdk.NewCoin("uphoton", overrideAmount)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = defaultFees
	k.SetParams(ctx, providerParams)

	// consumer1 gets an override; consumer0 keeps the default.
	require.NoError(t, k.ConsumerFeesPerBlockOverride.Set(ctx, consumer1, overrideAmount))

	consumer0FeePoolAddr := k.GetConsumerFeePoolAddress(consumer0)
	consumer1FeePoolAddr := k.GetConsumerFeePoolAddress(consumer1)

	gomock.InOrder(
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer0FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(defaultFees)).
			Return(nil),
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer1FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(overrideFees)).
			Return(nil),
	)

	total := k.CollectFeesFromConsumers(ctx)
	require.Equal(t, sdk.NewInt64Coin("uphoton", 35), total)
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
			SendCoinsFromAccountToModule(gomock.Any(), consumer0FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerBlock)).
			Return(sdkerrors.ErrInsufficientFunds.Wrapf("spendable 5 < 10")),
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer1FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerBlock)).
			Return(nil),
	)

	total := k.CollectFeesFromConsumers(ctx)
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

	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromAccountToModule(gomock.Any(), consumer0FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerBlock)).
		Return(nil)

	total := k.CollectFeesFromConsumers(ctx)
	require.Equal(t, sdk.NewInt64Coin("uphoton", 10), total)
	require.False(t, k.IsConsumerInDebt(ctx, consumer0))
}

// TestCollectFeesFromConsumersContinuesOnGenericError: a non-insufficient-
// funds error from the bank keeper (chain-config issue) is logged and
// skipped without flipping the debt flag — consumer0 remains not-in-debt
// because its pool is funded and the error is not its fault.
func TestCollectFeesFromConsumersContinuesOnGenericError(t *testing.T) {
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
			SendCoinsFromAccountToModule(gomock.Any(), consumer0FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerBlock)).
			Return(errors.New("bank send restriction")),
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer1FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerBlock)).
			Return(nil),
	)

	total := k.CollectFeesFromConsumers(ctx)
	require.Equal(t, sdk.NewInt64Coin("uphoton", 10), total)
	require.False(t, k.IsConsumerInDebt(ctx, consumer0))
	require.False(t, k.IsConsumerInDebt(ctx, consumer1))
}

// Helpers for the distribution tests.

// newBondedValidator builds a Validator with an operator address derived from
// opSeed. Returns the validator and its operator bytes.
func newBondedValidator(t *testing.T, codec addresscodec.Codec, opSeed byte) (stakingtypes.Validator, []byte) {
	t.Helper()
	opBytes := bytes.Repeat([]byte{opSeed}, 20)
	op, err := codec.BytesToString(opBytes)
	require.NoError(t, err)
	pk := ed25519.GenPrivKey().PubKey()
	val, err := stakingtypes.NewValidator(op, pk, stakingtypes.Description{})
	require.NoError(t, err)
	val.Status = stakingtypes.Bonded
	val.Tokens = sdk.DefaultPowerReduction
	val.DelegatorShares = math.LegacyNewDecFromInt(sdk.DefaultPowerReduction)
	return val, opBytes
}

// TestDistributeFeesToValidators splits the pool evenly among all bonded
// validators, independent of their stake (tokens differ but shares are equal).
func TestDistributeFeesToValidators(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, op1Bytes := newBondedValidator(t, valAddrCodec, 1)
	val1.Tokens = sdk.DefaultPowerReduction.MulRaw(10) // stake differs on purpose
	val2, op2Bytes := newBondedValidator(t, valAddrCodec, 2)
	val2.Tokens = sdk.DefaultPowerReduction.MulRaw(20)

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
		SendCoinsFromModuleToAccount(gomock.Any(), providertypes.ModuleName, sdk.AccAddress(op1Bytes), sdk.NewCoins(sdk.NewInt64Coin("uphoton", 150))).
		Return(nil)
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), providertypes.ModuleName, sdk.AccAddress(op2Bytes), sdk.NewCoins(sdk.NewInt64Coin("uphoton", 150))).
		Return(nil)

	require.NoError(t, k.DistributeFeesToValidators(ctx))
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

	require.NoError(t, k.DistributeFeesToValidators(ctx))
}

// TestDistributeFeesToValidatorsSkipsWhenFeesTooSmall: pool holds 1 coin,
// 2 validators → floor(1/2) = 0 → nothing sent, balance stays pooled.
func TestDistributeFeesToValidatorsSkipsWhenFeesTooSmall(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, _ := newBondedValidator(t, valAddrCodec, 1)
	val2, _ := newBondedValidator(t, valAddrCodec, 2)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = sdk.NewInt64Coin("uphoton", 1)
	k.SetParams(ctx, providerParams)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providerParams.FeesPerBlock.Denom).
		Return(sdk.NewInt64Coin("uphoton", 1))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)

	require.NoError(t, k.DistributeFeesToValidators(ctx))
}

// TestDistributeFeesToValidatorsRemainderStaysPooled: 10 coins / 3 validators
// → each gets floor(10/3) = 3, 1 coin stays in the pool.
func TestDistributeFeesToValidatorsRemainderStaysPooled(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, op1 := newBondedValidator(t, valAddrCodec, 1)
	val2, op2 := newBondedValidator(t, valAddrCodec, 2)
	val3, op3 := newBondedValidator(t, valAddrCodec, 3)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = sdk.NewInt64Coin("uphoton", 10)
	k.SetParams(ctx, providerParams)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providerParams.FeesPerBlock.Denom).
		Return(sdk.NewInt64Coin("uphoton", 10))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2, val3}, nil)
	share := sdk.NewCoins(sdk.NewInt64Coin("uphoton", 3))
	for _, op := range [][]byte{op1, op2, op3} {
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromModuleToAccount(gomock.Any(), providertypes.ModuleName, sdk.AccAddress(op), share).
			Return(nil)
	}

	require.NoError(t, k.DistributeFeesToValidators(ctx))
}

// TestDistributeFeesToValidatorsNoBondedValidators: no bonded validators → nothing sent.
func TestDistributeFeesToValidatorsNoBondedValidators(t *testing.T) {
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
		Return(nil, nil)

	require.NoError(t, k.DistributeFeesToValidators(ctx))
}

// TestDistributeFeesToValidatorsIncludesOfflineValidators: all bonded
// validators receive an equal share regardless of signing status.
func TestDistributeFeesToValidatorsIncludesOfflineValidators(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, op1 := newBondedValidator(t, valAddrCodec, 1)
	val2, op2 := newBondedValidator(t, valAddrCodec, 2)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = sdk.NewInt64Coin("uphoton", 10)
	k.SetParams(ctx, providerParams)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providerParams.FeesPerBlock.Denom).
		Return(sdk.NewInt64Coin("uphoton", 100))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)
	// Both validators get an equal share (100/2 = 50), even if one was offline.
	share := sdk.NewCoins(sdk.NewInt64Coin("uphoton", 50))
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), providertypes.ModuleName, sdk.AccAddress(op1), share).
		Return(nil)
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), providertypes.ModuleName, sdk.AccAddress(op2), share).
		Return(nil)

	require.NoError(t, k.DistributeFeesToValidators(ctx))
}

// TestDistributeFeesToValidatorsPropagatesBondedFetchError: error from the
// staking keeper is surfaced, not swallowed.
func TestDistributeFeesToValidatorsPropagatesBondedFetchError(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = sdk.NewInt64Coin("uphoton", 10)
	k.SetParams(ctx, providerParams)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providerParams.FeesPerBlock.Denom).
		Return(sdk.NewInt64Coin("uphoton", 100))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return(nil, errors.New("boom"))

	err := k.DistributeFeesToValidators(ctx)
	require.ErrorContains(t, err, "boom")
}
