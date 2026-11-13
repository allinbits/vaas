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

// TestFeePoolSendRestriction_BlocksUnsanctionedSenders ensures that the
// restriction blocks sends from module accounts and EOAs other than the two
// sanctioned ones (provider, distribution).
func TestFeePoolSendRestriction_BlocksUnsanctionedSenders(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	require.NoError(t, k.FeePoolAddressToConsumerId.Set(ctx, poolAddr, consumerId))
	restrict := k.FeePoolSendRestriction()
	amt := sdk.NewCoins(sdk.NewInt64Coin("uphoton", 1))

	senders := []sdk.AccAddress{
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
// fee pool from another consumer's fee pool is blocked — fee pool addresses
// are not on the sanctioned-sender list.
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

// TestFeePoolSendRestriction_DeletedConsumerRefusesEverySender: deletion keeps
// the reverse-lookup entry so the restriction keeps refusing sends to the pool
// of a deleted consumer, from every sender including the two a live pool
// exempts. The pool was settled by the auto-sweep and nothing sweeps it again,
// so funds reaching it would be stranded.
func TestFeePoolSendRestriction_DeletedConsumerRefusesEverySender(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(42)
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	require.NoError(t, k.FeePoolAddressToConsumerId.Set(ctx, poolAddr, consumerId))
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_DELETED)

	restrict := k.FeePoolSendRestriction()
	amt := sdk.NewCoins(sdk.NewInt64Coin("uphoton", 1))
	senders := []sdk.AccAddress{
		sdk.AccAddress([]byte("user____________")),
		authtypes.NewModuleAddress(providertypes.ModuleName),
		authtypes.NewModuleAddress(disttypes.ModuleName),
	}
	for _, from := range senders {
		_, err := restrict(ctx, from, poolAddr, amt)
		require.ErrorIs(t, err, providertypes.ErrInvalidPhase,
			"sender %s should be refused", from.String())
	}
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

// TestFeePoolSendRestriction_DistributionSenderAllowed: sends from the
// distribution module account to a fee pool are allowed (gov-fund path uses
// DistributeFromFeePool to deposit directly).
func TestFeePoolSendRestriction_DistributionSenderAllowed(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	require.NoError(t, k.FeePoolAddressToConsumerId.Set(ctx, poolAddr, consumerId))

	restrict := k.FeePoolSendRestriction()
	distrAddr := authtypes.NewModuleAddress(disttypes.ModuleName)
	amt := sdk.NewCoins(sdk.NewInt64Coin("uphoton", 1))

	to, err := restrict(ctx, distrAddr, poolAddr, amt)
	require.NoError(t, err)
	require.Equal(t, poolAddr, to)
}
