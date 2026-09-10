package keeper

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	coreaddress "cosmossdk.io/core/address"
	"cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govkeeper "github.com/cosmos/cosmos-sdk/x/gov/keeper"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// NewInMemGovKeeper builds a real gov keeper on the in-memory store of
// params, so tests can seed proposals into gov's own collections and drive
// the provider keeper's removal-vote scan through the code path production
// runs. Every gov dependency is a stub that panics if called: the scan only
// reads the proposal store and the active-proposals queue, and a test that
// strays into deposits, tallies, or message execution should fail loudly
// rather than pass against a stub.
func NewInMemGovKeeper(tb testing.TB, params InMemKeeperParams) *govkeeper.Keeper {
	tb.Helper()
	require.NotNil(tb, params.GovStoreKey, "params must come from NewInMemKeeperParams")
	return newInMemGovKeeper(params)
}

// newInMemGovKeeper is NewInMemGovKeeper without a test handle, for the
// provider keeper constructor.
func newInMemGovKeeper(params InMemKeeperParams) *govkeeper.Keeper {
	if params.GovStoreKey == nil {
		panic("InMemKeeperParams without a gov store key: use NewInMemKeeperParams")
	}
	authority := authtypes.NewModuleAddress(govtypes.ModuleName)
	return govkeeper.NewKeeper(
		params.Cdc,
		runtime.NewKVStoreService(params.GovStoreKey),
		govStubAccountKeeper{moduleAddr: authority},
		govStubBankKeeper{},
		govStubStakingKeeper{},
		govStubDistributionKeeper{},
		govStubRouter{},
		govtypes.DefaultConfig(),
		authority.String(),
	)
}

func govStubUnexpected(method string) {
	panic("gov stub: " + method + " is not exercised by the provider's removal-vote scan")
}

type govStubAccountKeeper struct {
	moduleAddr sdk.AccAddress
}

func (govStubAccountKeeper) AddressCodec() coreaddress.Codec {
	return address.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())
}

func (govStubAccountKeeper) GetAccount(context.Context, sdk.AccAddress) sdk.AccountI {
	govStubUnexpected("AccountKeeper.GetAccount")
	return nil
}

func (s govStubAccountKeeper) GetModuleAddress(string) sdk.AccAddress { return s.moduleAddr }

func (govStubAccountKeeper) GetModuleAccount(context.Context, string) sdk.ModuleAccountI {
	govStubUnexpected("AccountKeeper.GetModuleAccount")
	return nil
}

func (govStubAccountKeeper) SetModuleAccount(context.Context, sdk.ModuleAccountI) {
	govStubUnexpected("AccountKeeper.SetModuleAccount")
}

type govStubBankKeeper struct{}

func (govStubBankKeeper) GetAllBalances(context.Context, sdk.AccAddress) sdk.Coins {
	govStubUnexpected("BankKeeper.GetAllBalances")
	return nil
}

func (govStubBankKeeper) GetBalance(context.Context, sdk.AccAddress, string) sdk.Coin {
	govStubUnexpected("BankKeeper.GetBalance")
	return sdk.Coin{}
}

func (govStubBankKeeper) LockedCoins(context.Context, sdk.AccAddress) sdk.Coins {
	govStubUnexpected("BankKeeper.LockedCoins")
	return nil
}

func (govStubBankKeeper) SpendableCoins(context.Context, sdk.AccAddress) sdk.Coins {
	govStubUnexpected("BankKeeper.SpendableCoins")
	return nil
}

func (govStubBankKeeper) SendCoinsFromModuleToAccount(context.Context, string, sdk.AccAddress, sdk.Coins) error {
	govStubUnexpected("BankKeeper.SendCoinsFromModuleToAccount")
	return nil
}

func (govStubBankKeeper) SendCoinsFromAccountToModule(context.Context, sdk.AccAddress, string, sdk.Coins) error {
	govStubUnexpected("BankKeeper.SendCoinsFromAccountToModule")
	return nil
}

func (govStubBankKeeper) BurnCoins(context.Context, string, sdk.Coins) error {
	govStubUnexpected("BankKeeper.BurnCoins")
	return nil
}

type govStubStakingKeeper struct{}

func (govStubStakingKeeper) ValidatorAddressCodec() coreaddress.Codec {
	return address.NewBech32Codec(sdk.GetConfig().GetBech32ValidatorAddrPrefix())
}

func (govStubStakingKeeper) GetValidator(context.Context, sdk.ValAddress) (stakingtypes.Validator, error) {
	govStubUnexpected("StakingKeeper.GetValidator")
	return stakingtypes.Validator{}, nil
}

func (govStubStakingKeeper) GetDelegation(context.Context, sdk.AccAddress, sdk.ValAddress) (stakingtypes.Delegation, error) {
	govStubUnexpected("StakingKeeper.GetDelegation")
	return stakingtypes.Delegation{}, nil
}

func (govStubStakingKeeper) IterateBondedValidatorsByPower(context.Context, func(int64, stakingtypes.ValidatorI) bool) error {
	govStubUnexpected("StakingKeeper.IterateBondedValidatorsByPower")
	return nil
}

func (govStubStakingKeeper) TotalBondedTokens(context.Context) (math.Int, error) {
	govStubUnexpected("StakingKeeper.TotalBondedTokens")
	return math.Int{}, nil
}

func (govStubStakingKeeper) IterateDelegations(context.Context, sdk.AccAddress, func(int64, stakingtypes.DelegationI) bool) error {
	govStubUnexpected("StakingKeeper.IterateDelegations")
	return nil
}

type govStubDistributionKeeper struct{}

func (govStubDistributionKeeper) FundCommunityPool(context.Context, sdk.Coins, sdk.AccAddress) error {
	govStubUnexpected("DistributionKeeper.FundCommunityPool")
	return nil
}

type govStubRouter struct{}

func (govStubRouter) Handler(sdk.Msg) baseapp.MsgServiceHandler {
	govStubUnexpected("MessageRouter.Handler")
	return nil
}

func (govStubRouter) HandlerByTypeURL(string) baseapp.MsgServiceHandler {
	govStubUnexpected("MessageRouter.HandlerByTypeURL")
	return nil
}
