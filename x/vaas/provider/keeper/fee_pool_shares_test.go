package keeper_test

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	disttypes "github.com/cosmos/cosmos-sdk/x/distribution/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
)

const poolDenom = "uphoton"

// pow2 returns 2^n as a math.Int.
func pow2(n uint) math.Int {
	return math.NewIntFromBigInt(new(big.Int).Lsh(big.NewInt(1), n))
}

func TestComputeClaim(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	denom := poolDenom
	alice := sdk.AccAddress([]byte("alice___________"))

	// No shares yet: claim is zero
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 0))
	require.True(t, k.ComputeClaim(ctx, consumerId, alice, denom).IsZero())

	// Seed: alice has 100 shares of 100 total, balance 50
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, denom, alice), math.NewInt(100)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, denom), math.NewInt(100)))
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 50))
	require.Equal(t, math.NewInt(50), k.ComputeClaim(ctx, consumerId, alice, denom))

	// Escrow 20 against an unexpired withheld-fee record: only the unreserved
	// balance (50 - 20 = 30) is currently claimable.
	require.NoError(t, k.WithheldFeeRecords.Set(ctx,
		collections.Join(consumerId, []byte("val-escrow-holder__")), providertypes.WithheldFeeRecord{
			ConsumerId:       consumerId,
			ProviderConsAddr: []byte("val-escrow-holder__"),
			Amount:           sdk.NewInt64Coin(denom, 20),
			ExpiresAt:        ctx.BlockTime().Add(time.Hour),
		}))
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 50))
	require.Equal(t, math.NewInt(30), k.ComputeClaim(ctx, consumerId, alice, denom))
}

// TestFeePoolSharesConsistencyInvariant_UnaccountedBalance checks that the
// invariant reports a pool balance no shares account for -- reachable through
// the send restriction's distribution-module exemption -- and stops reporting it
// once a deposit has credited the community pool with that balance.
func TestFeePoolSharesConsistencyInvariant_UnaccountedBalance(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	denom := poolDenom
	alice := sdk.AccAddress([]byte("alice___________"))
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	require.NoError(t, k.FeePoolAddressToConsumerId.Set(ctx, poolAddr, consumerId))

	mocks.MockBankKeeper.EXPECT().GetAllBalances(ctx, poolAddr).
		Return(sdk.NewCoins(sdk.NewInt64Coin(denom, 100))).AnyTimes()
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).
		Return(sdk.NewInt64Coin(denom, 100)).AnyTimes()

	msg, broken := providerkeeper.FeePoolSharesConsistencyInvariant(k)(ctx)
	require.True(t, broken)
	require.Contains(t, msg, "with no shares")

	// A deposit books the unaccounted balance to the community pool, and shares
	// cover the balance again.
	require.NoError(t, k.MintShares(ctx, consumerId, alice, sdk.NewInt64Coin(denom, 100)))

	msg, broken = providerkeeper.FeePoolSharesConsistencyInvariant(k)(ctx)
	require.False(t, broken, msg)
}

// TestComputeClaim_SharesFarAboveBalance: the claim math takes its products
// over big.Int, so holdings far above the balance (2^250 shares against a
// million tokens) resolve to the exact pro-rata amount instead of overflowing.
func TestComputeClaim_SharesFarAboveBalance(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	denom := poolDenom
	alice := sdk.AccAddress([]byte("alice___________"))
	bob := sdk.AccAddress([]byte("bob_____________"))
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)

	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, denom, alice), pow2(250)))
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, denom, bob), pow2(250).MulRaw(3)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, denom), pow2(250).MulRaw(4)))
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).
		Return(sdk.NewInt64Coin(denom, 1_000_000)).Times(2)

	require.Equal(t, math.NewInt(250_000), k.ComputeClaim(ctx, consumerId, alice, denom))
	require.Equal(t, math.NewInt(750_000), k.ComputeClaim(ctx, consumerId, bob, denom))
}

// TestMintSharesRebasesDrainedPoolLedger drives the share ledger through the
// cycle that inflates it on a running chain: an epoch distribution draws the
// pool down to a small remainder without burning any shares (the balance move
// DistributeConsumerFees makes), a depositor tops the pool back up with the
// floor amount, and so on. Each top-up multiplies the shares-per-token ratio
// by about deposit / remainder, so the total crosses the rebase threshold
// within a handful of cycles. The ledger must rescale instead of growing past
// the threshold, keep its sum invariant, and keep every holder's claim within
// one token of an exact pro-rata model of the pool throughout.
func TestMintSharesRebasesDrainedPoolLedger(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	denom := poolDenom
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	require.NoError(t, k.FeePoolAddressToConsumerId.Set(ctx, poolAddr, consumerId))
	holders := []sdk.AccAddress{
		sdk.AccAddress([]byte("alice___________")),
		sdk.AccAddress([]byte("bob_____________")),
	}

	// The pool balance the bank reports, driven from here.
	balance := math.ZeroInt()
	mocks.MockBankKeeper.EXPECT().GetBalance(gomock.Any(), poolAddr, denom).
		DoAndReturn(func(context.Context, sdk.AccAddress, string) sdk.Coin {
			return sdk.NewCoin(denom, balance)
		}).AnyTimes()
	mocks.MockBankKeeper.EXPECT().GetAllBalances(gomock.Any(), poolAddr).
		DoAndReturn(func(context.Context, sdk.AccAddress) sdk.Coins {
			return sdk.NewCoins(sdk.NewCoin(denom, balance))
		}).AnyTimes()

	// Exact pro-rata model: each holder's ideal balance as a rational.
	ideal := make([]*big.Rat, len(holders))
	for i := range ideal {
		ideal[i] = new(big.Rat)
	}
	requireClaimsMatchModel := func(step string) {
		t.Helper()
		for i, holder := range holders {
			claim := k.ComputeClaim(ctx, consumerId, holder, denom).BigInt()
			expected := new(big.Int).Quo(ideal[i].Num(), ideal[i].Denom())
			diff := new(big.Int).Abs(new(big.Int).Sub(claim, expected))
			require.LessOrEqual(t, diff.Cmp(big.NewInt(1)), 0,
				"%s: holder %d claims %s, model says %s", step, i, claim, expected)
		}
		msg, broken := providerkeeper.FeePoolSharesConsistencyInvariant(k)(ctx)
		require.False(t, broken, "%s: %s", step, msg)
	}

	// One above the default floor, fees_per_block * min_deposit_blocks.
	deposit := math.NewInt(14_400_001)
	remainder := math.NewInt(1000)
	threshold := new(big.Int).Lsh(big.NewInt(1), 128)
	totalKey := collections.Join(consumerId, denom)

	rebases := 0
	for cycle := 0; cycle < 24; cycle++ {
		depositor := cycle % len(holders)

		before := math.ZeroInt()
		if cycle > 0 {
			var err error
			before, err = k.ConsumerFeePoolTotalShares.Get(ctx, totalKey)
			require.NoError(t, err)
		}
		// MintShares prices the deposit against the balance before it lands.
		require.NoError(t, k.MintShares(ctx, consumerId, holders[depositor], sdk.NewCoin(denom, deposit)))
		balance = balance.Add(deposit)
		ideal[depositor].Add(ideal[depositor], new(big.Rat).SetInt(deposit.BigInt()))

		after, err := k.ConsumerFeePoolTotalShares.Get(ctx, totalKey)
		require.NoError(t, err)
		require.LessOrEqual(t, after.BigInt().Cmp(threshold), 0,
			"cycle %d: total %s passed the rebase threshold", cycle, after)
		if after.LT(before) {
			rebases++
		}
		requireClaimsMatchModel("after the deposit of cycle " + string(rune('0'+cycle%10)))

		// The epoch distributions draw the pool down to the remainder without
		// touching the ledger; every holder's stake shrinks pro rata.
		scale := new(big.Rat).SetFrac(remainder.BigInt(), balance.BigInt())
		for i := range ideal {
			ideal[i].Mul(ideal[i], scale)
		}
		balance = remainder
		requireClaimsMatchModel("after the drain of cycle " + string(rune('0'+cycle%10)))
	}
	require.GreaterOrEqual(t, rebases, 2, "the cycle must have crossed the rebase threshold more than once")
}

// TestMintSharesRefusesShareTotalPastBound: a rebase keeps at least 2^32
// shares per token, so the share total can only pass 2^200 when the balance
// itself approaches 2^168; the drain-and-redeposit cycle never gets there
// because the rebase intercepts it at 2^128. The state is seeded directly:
// alice holds 2^199 shares against a balance of 2^170, where the ratio 2^29
// leaves nothing to rescale. A deposit of 2^170 lands the total exactly on
// the bound and is accepted; one token more would mint 2^199 + 2^29 shares,
// pass the bound and is refused with the ledger untouched.
func TestMintSharesRefusesShareTotalPastBound(t *testing.T) {
	consumerId := uint64(0)
	denom := poolDenom
	alice := sdk.AccAddress([]byte("alice___________"))
	bob := sdk.AccAddress([]byte("bob_____________"))

	cases := []struct {
		name      string
		deposit   math.Int
		wantErr   error
		wantTotal math.Int
	}{
		{
			name:      "deposit landing on the bound is accepted",
			deposit:   pow2(170),
			wantTotal: pow2(200),
		},
		{
			name:      "deposit passing the bound is refused",
			deposit:   pow2(170).AddRaw(1),
			wantErr:   providertypes.ErrShareLedgerOverflow,
			wantTotal: pow2(199),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
			defer ctrl.Finish()
			poolAddr := k.GetConsumerFeePoolAddress(consumerId)

			require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
				collections.Join3(consumerId, denom, alice), pow2(199)))
			require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
				collections.Join(consumerId, denom), pow2(199)))
			mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).
				Return(sdk.NewCoin(denom, pow2(170)))

			err := k.MintShares(ctx, consumerId, bob, sdk.NewCoin(denom, tc.deposit))
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				_, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, bob))
				require.ErrorIs(t, err, collections.ErrNotFound)
			} else {
				require.NoError(t, err)
			}
			aliceShares, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, alice))
			require.NoError(t, err)
			require.Equal(t, pow2(199), aliceShares)
			total, err := k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
			require.NoError(t, err)
			require.Equal(t, tc.wantTotal, total)
		})
	}
}

func TestMintShares(t *testing.T) {
	consumerId := uint64(0)
	denom := poolDenom
	alice := sdk.AccAddress([]byte("alice___________"))
	bob := sdk.AccAddress([]byte("bob_____________"))
	distrAddr := authtypes.NewModuleAddress(disttypes.ModuleName)

	testCases := []struct {
		name      string
		setup     func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, poolAddr sdk.AccAddress)
		deposit   sdk.Coin
		depositor sdk.AccAddress
		wantErr   error
		postCheck func(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context)
	}{
		{
			name:      "initial deposit mints amount as shares",
			depositor: alice,
			deposit:   sdk.NewInt64Coin(denom, 100),
			setup: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, poolAddr sdk.AccAddress) {
				// Initial deposit: total_shares == 0; mint = amount
				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 0))
			},
			postCheck: func(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context) {
				t.Helper()
				shares, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, alice))
				require.NoError(t, err)
				require.Equal(t, math.NewInt(100), shares)

				total, err := k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
				require.NoError(t, err)
				require.Equal(t, math.NewInt(100), total)
			},
		},
		{
			name:      "subsequent deposit proportional to existing shares",
			depositor: bob,
			deposit:   sdk.NewInt64Coin(denom, 100),
			setup: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, poolAddr sdk.AccAddress) {
				// Seed: alice has 100 shares against balance 50 (consumed via fees)
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, alice), math.NewInt(100)))
				require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
					collections.Join(consumerId, denom), math.NewInt(100)))
				// Bob deposits 100 when balance is 50 (PRE-deposit) and 150 (POST-deposit)
				// The mint formula uses balance BEFORE the new deposit lands.
				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 50))
			},
			postCheck: func(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context) {
				t.Helper()
				// Bob: shares = 100 * 100 / 50 = 200
				shares, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, bob))
				require.NoError(t, err)
				require.Equal(t, math.NewInt(200), shares)

				total, err := k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
				require.NoError(t, err)
				require.Equal(t, math.NewInt(300), total)
			},
		},
		{
			name:      "zero balance triggers lazy invalidation of existing shares",
			depositor: bob,
			deposit:   sdk.NewInt64Coin(denom, 50),
			setup: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, poolAddr sdk.AccAddress) {
				// Seed: alice has 100 shares, balance is 0 (pool fully consumed by fees)
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, alice), math.NewInt(100)))
				require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
					collections.Join(consumerId, denom), math.NewInt(100)))
				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 0))
			},
			postCheck: func(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context) {
				t.Helper()
				// Alice's shares should be wiped (lazy invalidation), Bob's recorded as initial
				_, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, alice))
				require.ErrorIs(t, err, collections.ErrNotFound)

				bobShares, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, bob))
				require.NoError(t, err)
				require.Equal(t, math.NewInt(50), bobShares)

				total, err := k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
				require.NoError(t, err)
				require.Equal(t, math.NewInt(50), total)
			},
		},
		{
			name:      "unaccounted balance is credited to the community pool",
			depositor: bob,
			deposit:   sdk.NewInt64Coin(denom, 50),
			setup: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, poolAddr sdk.AccAddress) {
				// No share records at all, yet the pool holds 100: a
				// community-pool spend addressed the pool directly.
				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 100))
			},
			postCheck: func(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context) {
				t.Helper()
				// The 100 is booked to the distribution module account, so bob's
				// 50 mints at par against it instead of buying into it.
				distrShares, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, distrAddr))
				require.NoError(t, err)
				require.Equal(t, math.NewInt(100), distrShares)

				bobShares, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, bob))
				require.NoError(t, err)
				require.Equal(t, math.NewInt(50), bobShares)

				total, err := k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
				require.NoError(t, err)
				require.Equal(t, math.NewInt(150), total)
			},
		},
		{
			name:      "share record without a stored total is dropped, not re-valued",
			depositor: bob,
			deposit:   sdk.NewInt64Coin(denom, 50),
			setup: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, poolAddr sdk.AccAddress) {
				// A share record with no total behind it backs no claim: every
				// reader treats a missing total as an empty pool.
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, alice), math.NewInt(999)))
				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 100))
			},
			postCheck: func(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context) {
				t.Helper()
				_, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, alice))
				require.ErrorIs(t, err, collections.ErrNotFound)

				distrShares, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, distrAddr))
				require.NoError(t, err)
				require.Equal(t, math.NewInt(100), distrShares)

				total, err := k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
				require.NoError(t, err)
				require.Equal(t, math.NewInt(150), total)
			},
		},
		{
			name:      "sub-share deposit rejected",
			depositor: bob,
			deposit:   sdk.NewInt64Coin(denom, 1),
			setup: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, poolAddr sdk.AccAddress) {
				// Seed: alice has 1_000_000 shares of 1_000_000 total, balance is huge (1_000_000_000).
				// Bob's tiny 1-unit deposit would mint floor(1 * 1_000_000 / 1_000_000_000) = 0 shares.
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, alice), math.NewInt(1_000_000)))
				require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
					collections.Join(consumerId, denom), math.NewInt(1_000_000)))
				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 1_000_000_000))
			},
			wantErr: providertypes.ErrDepositTooSmall,
			postCheck: func(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context) {
				t.Helper()
				// No state mutation: bob has no entry, total unchanged, alice unchanged.
				_, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, bob))
				require.ErrorIs(t, err, collections.ErrNotFound)

				aliceShares, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, alice))
				require.NoError(t, err)
				require.Equal(t, math.NewInt(1_000_000), aliceShares)

				total, err := k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
				require.NoError(t, err)
				require.Equal(t, math.NewInt(1_000_000), total)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
			defer ctrl.Finish()

			poolAddr := k.GetConsumerFeePoolAddress(consumerId)
			if tc.setup != nil {
				tc.setup(k, ctx, mocks, poolAddr)
			}

			err := k.MintShares(ctx, consumerId, tc.depositor, tc.deposit)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}

			if tc.postCheck != nil {
				tc.postCheck(t, k, ctx)
			}
		})
	}
}

func TestWithdrawShares(t *testing.T) {
	consumerId := uint64(0)
	denom := poolDenom
	alice := sdk.AccAddress([]byte("alice___________"))
	bob := sdk.AccAddress([]byte("bob_____________"))

	testCases := []struct {
		name      string
		setup     func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, poolAddr sdk.AccAddress)
		withdraw  sdk.Coin
		depositor sdk.AccAddress
		wantErr   error
		postCheck func(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context, tokens sdk.Coin)
	}{
		{
			name:      "full withdraw burns all shares when over claim",
			depositor: alice,
			withdraw:  sdk.NewInt64Coin(denom, 200),
			setup: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, poolAddr sdk.AccAddress) {
				// Alice sole depositor with 100 shares against balance 100
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, alice), math.NewInt(100)))
				require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
					collections.Join(consumerId, denom), math.NewInt(100)))
				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 100))
			},
			// Request 200 (over claim) — should burn all shares, return claim=100
			postCheck: func(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context, tokens sdk.Coin) {
				t.Helper()
				require.Equal(t, math.NewInt(100), tokens.Amount)

				_, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, alice))
				require.ErrorIs(t, err, collections.ErrNotFound)

				_, err = k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
				require.ErrorIs(t, err, collections.ErrNotFound)
			},
		},
		{
			name:      "partial withdraw updates shares",
			depositor: alice,
			withdraw:  sdk.NewInt64Coin(denom, 50),
			setup: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, poolAddr sdk.AccAddress) {
				// Two depositors, balance 200, total 200
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, alice), math.NewInt(100)))
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, bob), math.NewInt(100)))
				require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
					collections.Join(consumerId, denom), math.NewInt(200)))
				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 200))
			},
			// Alice withdraws 50 (partial, well below claim 100)
			postCheck: func(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context, tokens sdk.Coin) {
				t.Helper()
				require.Equal(t, math.NewInt(50), tokens.Amount)

				// Alice burned 50 shares; total = 150; alice = 50
				aliceShares, _ := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, alice))
				require.Equal(t, math.NewInt(50), aliceShares)
				total, _ := k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
				require.Equal(t, math.NewInt(150), total)
			},
		},
		{
			name:      "withheld escrow caps the withdrawal and leaves residual shares",
			depositor: alice,
			withdraw:  sdk.NewInt64Coin(denom, 100),
			setup: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, poolAddr sdk.AccAddress) {
				// Alice sole depositor: 100 shares against balance 100, but 40
				// is escrowed against an unexpired withheld-fee record, so only
				// 60 is unreserved.
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, alice), math.NewInt(100)))
				require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
					collections.Join(consumerId, denom), math.NewInt(100)))
				require.NoError(t, k.WithheldFeeRecords.Set(ctx,
					collections.Join(consumerId, []byte("val-escrow-holder__")), providertypes.WithheldFeeRecord{
						ConsumerId:       consumerId,
						ProviderConsAddr: []byte("val-escrow-holder__"),
						Amount:           sdk.NewInt64Coin(denom, 40),
						ExpiresAt:        ctx.BlockTime().Add(time.Hour),
					}))
				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 100))
			},
			// Request 100 (the whole claim) but only 60 is unreserved: deliver
			// 60, burning shares at the full-balance rate so alice keeps 40
			// shares backing the escrow -- no orphan balance is created.
			postCheck: func(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context, tokens sdk.Coin) {
				t.Helper()
				require.Equal(t, math.NewInt(60), tokens.Amount)

				aliceShares, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, alice))
				require.NoError(t, err)
				require.Equal(t, math.NewInt(40), aliceShares)

				total, err := k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
				require.NoError(t, err)
				require.Equal(t, math.NewInt(40), total)
			},
		},
		{
			name:      "shares far above the balance: full withdraw delivers the claim",
			depositor: alice,
			withdraw:  sdk.NewInt64Coin(denom, 500_000),
			setup: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, poolAddr sdk.AccAddress) {
				// Alice and bob hold 2^250 shares each against a balance of a
				// million: shares * balance passes 256 bits, the claim does not.
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, alice), pow2(250)))
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, bob), pow2(250)))
				require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
					collections.Join(consumerId, denom), pow2(251)))
				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 1_000_000))
			},
			postCheck: func(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context, tokens sdk.Coin) {
				t.Helper()
				require.Equal(t, math.NewInt(500_000), tokens.Amount)

				_, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, alice))
				require.ErrorIs(t, err, collections.ErrNotFound)
				total, err := k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
				require.NoError(t, err)
				require.Equal(t, pow2(250), total)
			},
		},
		{
			name:      "shares far above the balance: partial withdraw burns at the full-balance rate",
			depositor: alice,
			withdraw:  sdk.NewInt64Coin(denom, 100_000),
			setup: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, poolAddr sdk.AccAddress) {
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, alice), pow2(250)))
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, bob), pow2(250)))
				require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
					collections.Join(consumerId, denom), pow2(251)))
				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 1_000_000))
			},
			postCheck: func(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context, tokens sdk.Coin) {
				t.Helper()
				// sharesToBurn = 100_000 * 2^251 / 1_000_000, and the tokens are
				// re-derived from them: 99_999 after truncation.
				burn := new(big.Int).Mul(big.NewInt(100_000), pow2(251).BigInt())
				burn.Quo(burn, big.NewInt(1_000_000))
				delivered := new(big.Int).Mul(burn, big.NewInt(1_000_000))
				delivered.Quo(delivered, pow2(251).BigInt())
				require.Equal(t, math.NewInt(99_999), math.NewIntFromBigInt(delivered))
				require.Equal(t, math.NewIntFromBigInt(delivered), tokens.Amount)

				aliceShares, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, alice))
				require.NoError(t, err)
				require.Equal(t, pow2(250).Sub(math.NewIntFromBigInt(burn)), aliceShares)
				total, err := k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
				require.NoError(t, err)
				require.Equal(t, pow2(251).Sub(math.NewIntFromBigInt(burn)), total)
			},
		},
		{
			name:      "zero pool balance returns ErrPoolEmpty",
			depositor: alice,
			withdraw:  sdk.NewInt64Coin(denom, 50),
			setup: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, poolAddr sdk.AccAddress) {
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, alice), math.NewInt(100)))
				require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
					collections.Join(consumerId, denom), math.NewInt(100)))
				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 0))
			},
			wantErr: providertypes.ErrPoolEmpty,
		},
		{
			name:      "sub-share withdraw returns ErrSubShareWithdraw without state mutation",
			depositor: alice,
			withdraw:  sdk.NewInt64Coin(denom, 1),
			setup: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, poolAddr sdk.AccAddress) {
				// Seed: alice has 100 shares of 100 total against a huge balance (1_000_000).
				// Claim = 100 * 1_000_000 / 100 = 1_000_000. A tiny 1-unit withdrawal hits
				// the partial branch and computes sharesToBurn = floor(1 * 100 / 1_000_000) = 0,
				// which must trigger the sub-share guard.
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, alice), math.NewInt(100)))
				require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
					collections.Join(consumerId, denom), math.NewInt(100)))
				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 1_000_000))
			},
			wantErr: providertypes.ErrSubShareWithdraw,
			postCheck: func(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context, tokens sdk.Coin) {
				t.Helper()
				require.Equal(t, sdk.Coin{}, tokens)

				// No state mutation: alice still has 100 shares, total still 100.
				aliceShares, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, alice))
				require.NoError(t, err)
				require.Equal(t, math.NewInt(100), aliceShares)

				total, err := k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
				require.NoError(t, err)
				require.Equal(t, math.NewInt(100), total)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
			defer ctrl.Finish()

			poolAddr := k.GetConsumerFeePoolAddress(consumerId)
			if tc.setup != nil {
				tc.setup(k, ctx, mocks, poolAddr)
			}

			tokens, err := k.WithdrawShares(ctx, consumerId, tc.depositor, tc.withdraw)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}

			if tc.postCheck != nil {
				tc.postCheck(t, k, ctx, tokens)
			}
		})
	}
}

func TestSweepConsumerFeePoolDenom(t *testing.T) {
	consumerId := uint64(0)
	denom := poolDenom
	alice := sdk.AccAddress([]byte("alice___________"))
	bob := sdk.AccAddress([]byte("bob_____________"))
	distrAddr := authtypes.NewModuleAddress(disttypes.ModuleName)
	providerModuleName := providertypes.ModuleName
	providerModuleAddr := authtypes.NewModuleAddress(providertypes.ModuleName)

	testCases := []struct {
		name       string
		setupMocks func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, poolAddr sdk.AccAddress)
		postCheck  func(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context)
	}{
		{
			name: "no dust: alice 30 bob 70 balance 100",
			setupMocks: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, poolAddr sdk.AccAddress) {
				// alice 30, bob 70, total 100, balance 100 — no dust
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, alice), math.NewInt(30)))
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, bob), math.NewInt(70)))
				require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
					collections.Join(consumerId, denom), math.NewInt(100)))

				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 100))
				mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
					ctx, poolAddr, providerModuleName, sdk.NewCoins(sdk.NewInt64Coin(denom, 100))).Return(nil)
				mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
					ctx, providerModuleName, alice, sdk.NewCoins(sdk.NewInt64Coin(denom, 30))).Return(nil)
				mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
					ctx, providerModuleName, bob, sdk.NewCoins(sdk.NewInt64Coin(denom, 70))).Return(nil)
				// No dust -> no FundCommunityPool call
			},
			postCheck: func(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context) {
				t.Helper()
				// All share records and total cleared
				_, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, alice))
				require.ErrorIs(t, err, collections.ErrNotFound)
				_, err = k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, bob))
				require.ErrorIs(t, err, collections.ErrNotFound)
				_, err = k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
				require.ErrorIs(t, err, collections.ErrNotFound)
			},
		},
		{
			name: "with dust: alice 1 bob 2 balance 10",
			setupMocks: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, poolAddr sdk.AccAddress) {
				// alice 1, bob 2, total 3, balance 10
				// alice claim: floor(1*10/3) = 3; bob claim: floor(2*10/3) = 6; dust = 1
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, alice), math.NewInt(1)))
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, bob), math.NewInt(2)))
				require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
					collections.Join(consumerId, denom), math.NewInt(3)))

				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 10))
				mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
					ctx, poolAddr, providerModuleName, sdk.NewCoins(sdk.NewInt64Coin(denom, 10))).Return(nil)
				mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
					ctx, providerModuleName, alice, sdk.NewCoins(sdk.NewInt64Coin(denom, 3))).Return(nil)
				mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
					ctx, providerModuleName, bob, sdk.NewCoins(sdk.NewInt64Coin(denom, 6))).Return(nil)
				mocks.MockDistributionKeeper.EXPECT().FundCommunityPool(
					ctx, sdk.NewCoins(sdk.NewInt64Coin(denom, 1)),
					providerModuleAddr).Return(nil)
			},
		},
		{
			name: "distribution module recipient uses community pool",
			setupMocks: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, poolAddr sdk.AccAddress) {
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, distrAddr), math.NewInt(100)))
				require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
					collections.Join(consumerId, denom), math.NewInt(100)))

				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 100))
				mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
					ctx, poolAddr, providerModuleName, sdk.NewCoins(sdk.NewInt64Coin(denom, 100))).Return(nil)
				// Distribution module account share goes via FundCommunityPool, NOT raw bank send
				mocks.MockDistributionKeeper.EXPECT().FundCommunityPool(
					ctx, sdk.NewCoins(sdk.NewInt64Coin(denom, 100)),
					providerModuleAddr).Return(nil)
			},
		},
		{
			name: "shares far above the balance: alice 2^250 bob 3 * 2^250 balance 1_000_000",
			setupMocks: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, poolAddr sdk.AccAddress) {
				// shares * balance passes 256 bits; each slice is the exact
				// pro-rata amount and nothing is left as dust.
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, alice), pow2(250)))
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, bob), pow2(250).MulRaw(3)))
				require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
					collections.Join(consumerId, denom), pow2(252)))

				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 1_000_000))
				mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
					ctx, poolAddr, providerModuleName, sdk.NewCoins(sdk.NewInt64Coin(denom, 1_000_000))).Return(nil)
				mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
					ctx, providerModuleName, alice, sdk.NewCoins(sdk.NewInt64Coin(denom, 250_000))).Return(nil)
				mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
					ctx, providerModuleName, bob, sdk.NewCoins(sdk.NewInt64Coin(denom, 750_000))).Return(nil)
			},
			postCheck: func(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context) {
				t.Helper()
				_, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, alice))
				require.ErrorIs(t, err, collections.ErrNotFound)
				_, err = k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, bob))
				require.ErrorIs(t, err, collections.ErrNotFound)
				_, err = k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
				require.ErrorIs(t, err, collections.ErrNotFound)
			},
		},
		{
			name: "unaccounted balance is swept to the community pool",
			setupMocks: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, poolAddr sdk.AccAddress) {
				// No share records at all, balance 100: the sweep credits the
				// community pool with it and pays it straight back out there.
				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 100))
				mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
					ctx, poolAddr, providerModuleName, sdk.NewCoins(sdk.NewInt64Coin(denom, 100))).Return(nil)
				mocks.MockDistributionKeeper.EXPECT().FundCommunityPool(
					ctx, sdk.NewCoins(sdk.NewInt64Coin(denom, 100)),
					providerModuleAddr).Return(nil)
			},
			postCheck: func(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context) {
				t.Helper()
				// Nothing left behind, including the absorbed share record.
				_, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, distrAddr))
				require.ErrorIs(t, err, collections.ErrNotFound)
				_, err = k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
				require.ErrorIs(t, err, collections.ErrNotFound)
			},
		},
		{
			name: "all shares floor to zero: dust routed to community pool and state cleared",
			setupMocks: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, poolAddr sdk.AccAddress) {
				// alice 1 share, bob 1 share, total 2, balance 1.
				// alice slice: floor(1*1/2) = 0 -> skipped
				// bob slice:   floor(1*1/2) = 0 -> skipped
				// distributed = 0; dust = 1 -> entire balance routed to community pool.
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, alice), math.NewInt(1)))
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, bob), math.NewInt(1)))
				require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
					collections.Join(consumerId, denom), math.NewInt(2)))

				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 1))
				mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
					ctx, poolAddr, providerModuleName, sdk.NewCoins(sdk.NewInt64Coin(denom, 1))).Return(nil)
				// No per-holder SendCoinsFromModuleToAccount: every slice floors to zero.
				mocks.MockDistributionKeeper.EXPECT().FundCommunityPool(
					ctx, sdk.NewCoins(sdk.NewInt64Coin(denom, 1)),
					providerModuleAddr).Return(nil)
			},
			postCheck: func(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context) {
				t.Helper()
				// All share records and total cleared.
				_, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, alice))
				require.ErrorIs(t, err, collections.ErrNotFound)
				_, err = k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, denom, bob))
				require.ErrorIs(t, err, collections.ErrNotFound)
				_, err = k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
				require.ErrorIs(t, err, collections.ErrNotFound)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
			defer ctrl.Finish()

			poolAddr := k.GetConsumerFeePoolAddress(consumerId)
			if tc.setupMocks != nil {
				tc.setupMocks(k, ctx, mocks, poolAddr)
			}

			k.SweepConsumerFeePoolDenom(ctx, consumerId, denom)

			if tc.postCheck != nil {
				tc.postCheck(t, k, ctx)
			}
		})
	}
}

func TestSweepConsumerFeePool_AllDenoms(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	alice := sdk.AccAddress([]byte("alice___________"))
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)

	// alice has shares in two denoms
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, poolDenom, alice), math.NewInt(10)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, poolDenom), math.NewInt(10)))
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, "uatone", alice), math.NewInt(5)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, "uatone"), math.NewInt(5)))

	mocks.MockBankKeeper.EXPECT().GetAllBalances(ctx, poolAddr).Return(
		sdk.NewCoins(sdk.NewInt64Coin(poolDenom, 10), sdk.NewInt64Coin("uatone", 5)))

	// Two per-denom sweeps. Expect bank ops for each.
	for _, c := range []sdk.Coin{
		sdk.NewInt64Coin("uatone", 5),
		sdk.NewInt64Coin(poolDenom, 10),
	} {
		mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, c.Denom).Return(c)
		mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
			ctx, poolAddr, providertypes.ModuleName, sdk.NewCoins(c)).Return(nil)
		mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
			ctx, providertypes.ModuleName, alice, sdk.NewCoins(c)).Return(nil)
	}

	k.SweepConsumerFeePool(ctx, consumerId, nil)
}
