package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	disttypes "github.com/cosmos/cosmos-sdk/x/distribution/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
)

func TestFeePoolSendRestriction(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
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

// TestFeePoolSendRestriction_BlocksAllNonProviderSenders ensures that the
// restriction blocks sends from arbitrary module accounts (gov, distribution,
// IBC transfer, etc.), not only from end-user EOAs. Only the provider module
// is whitelisted.
func TestFeePoolSendRestriction_BlocksAllNonProviderSenders(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	require.NoError(t, k.FeePoolAddressToConsumerId.Set(ctx, poolAddr, consumerId))
	restrict := k.FeePoolSendRestriction()
	amt := sdk.NewCoins(sdk.NewInt64Coin("uphoton", 1))

	// Cosmos-SDK module accounts and an arbitrary EOA must all be rejected.
	senders := []sdk.AccAddress{
		authtypes.NewModuleAddress(disttypes.ModuleName),
		authtypes.NewModuleAddress("gov"),
		authtypes.NewModuleAddress("transfer"),
		sdk.AccAddress([]byte("attacker________")),
	}
	for _, from := range senders {
		_, err := restrict(ctx, from, poolAddr, amt)
		require.ErrorIs(t, err, providertypes.ErrUnsolicitedFeePoolDeposit,
			"sender %s should be blocked", from.String())
	}
}

// TestFeePoolSendRestriction_CrossConsumerIsolation: a send to one consumer's
// fee pool from another consumer's fee pool is still blocked (only the
// provider module bypasses the restriction).
func TestFeePoolSendRestriction_CrossConsumerIsolation(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	poolA := k.GetConsumerFeePoolAddress(0)
	poolB := k.GetConsumerFeePoolAddress(1)
	require.NoError(t, k.FeePoolAddressToConsumerId.Set(ctx, poolA, uint64(0)))
	require.NoError(t, k.FeePoolAddressToConsumerId.Set(ctx, poolB, uint64(1)))

	restrict := k.FeePoolSendRestriction()
	amt := sdk.NewCoins(sdk.NewInt64Coin("uphoton", 1))
	_, err := restrict(ctx, poolA, poolB, amt)
	require.ErrorIs(t, err, providertypes.ErrUnsolicitedFeePoolDeposit)
}

// TestFeePoolSendRestriction_DeletedConsumerPassesThrough: once a consumer is
// deleted its reverse-lookup entry is gone, so sends to its (now-orphan)
// pool address pass through. This is the documented behavior — funds sent
// after delete are an unrecoverable user error, but the bank layer doesn't
// block them.
func TestFeePoolSendRestriction_DeletedConsumerPassesThrough(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	deletedPool := k.GetConsumerFeePoolAddress(42)
	// Reverse-lookup entry intentionally NOT set.
	restrict := k.FeePoolSendRestriction()
	user := sdk.AccAddress([]byte("user____________"))
	amt := sdk.NewCoins(sdk.NewInt64Coin("uphoton", 1))

	to, err := restrict(ctx, user, deletedPool, amt)
	require.NoError(t, err)
	require.Equal(t, deletedPool, to)
}

// TestFeePoolSendRestriction_OutboundFromPoolUnaffected: sends FROM a fee
// pool (fee collection, withdraw, sweep) pass regardless of the destination.
func TestFeePoolSendRestriction_OutboundFromPoolUnaffected(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	require.NoError(t, k.FeePoolAddressToConsumerId.Set(ctx, poolAddr, consumerId))

	restrict := k.FeePoolSendRestriction()
	recipient := sdk.AccAddress([]byte("recipient_______"))
	amt := sdk.NewCoins(sdk.NewInt64Coin("uphoton", 1))

	to, err := restrict(ctx, poolAddr, recipient, amt)
	require.NoError(t, err)
	require.Equal(t, recipient, to)
}

// TestFeePoolSendRestriction_GovDistributionFundingBlocked: gov funding from
// the distribution module account goes through `DistributeFromFeePool` →
// provider module → fee pool. The first hop's destination is the provider
// module account (not a pool), so the restriction passes that hop. A direct
// send from the distribution module to a pool, however, is NOT sanctioned
// and must be blocked.
func TestFeePoolSendRestriction_GovDistributionFundingBlocked(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	require.NoError(t, k.FeePoolAddressToConsumerId.Set(ctx, poolAddr, consumerId))

	restrict := k.FeePoolSendRestriction()
	distrAddr := authtypes.NewModuleAddress(disttypes.ModuleName)
	providerAddr := authtypes.NewModuleAddress(providertypes.ModuleName)
	amt := sdk.NewCoins(sdk.NewInt64Coin("uphoton", 1))

	// distribution → provider module: passes (destination not a pool).
	to, err := restrict(ctx, distrAddr, providerAddr, amt)
	require.NoError(t, err)
	require.Equal(t, providerAddr, to)

	// distribution → pool directly: blocked.
	_, err = restrict(ctx, distrAddr, poolAddr, amt)
	require.ErrorIs(t, err, providertypes.ErrUnsolicitedFeePoolDeposit)
}
