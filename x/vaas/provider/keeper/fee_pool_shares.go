package keeper

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/allinbits/vaas/x/vaas/provider/types"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	disttypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
)

// emitSweepEvent emits one ConsumerFeePoolSweep event per swept denom.
func (k Keeper) emitSweepEvent(
	ctx sdk.Context, consumerId uint64, denom string, distributed, dust math.Int,
) {
	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeConsumerFeePoolSweep,
		sdk.NewAttribute(types.AttributeConsumerId, strconv.FormatUint(consumerId, 10)),
		sdk.NewAttribute(types.AttributeDenom, denom),
		sdk.NewAttribute(types.AttributeTotalDistributed, distributed.String()),
		sdk.NewAttribute(types.AttributeDust, dust.String()),
	))
}

// ComputeClaim returns the depositor's claimable tokens for one denom on the
// given consumer's fee pool. Returns math.ZeroInt() if the depositor has no
// shares or total_shares is zero.
func (k Keeper) ComputeClaim(
	ctx sdk.Context, consumerId uint64, depositor sdk.AccAddress, denom string,
) math.Int {
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	balance := k.bankKeeper.GetBalance(ctx, poolAddr, denom)
	if balance.Amount.IsZero() {
		return math.ZeroInt()
	}
	shares, err := k.ConsumerFeePoolShares.Get(ctx,
		collections.Join3(consumerId, denom, depositor))
	if err != nil {
		return math.ZeroInt()
	}
	total, err := k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
	if err != nil || total.IsZero() {
		return math.ZeroInt()
	}
	// claim = floor(shares * balance / total)
	return shares.Mul(balance.Amount).Quo(total)
}

// MintShares credits the depositor with shares for the given amount in the
// specified consumer's fee pool. Handles the lazy-invalidation case:
// if balance == 0 but total_shares > 0, all existing shares for this
// (consumer, denom) are deleted first (they represent worthless claims).
//
// Caller is responsible for the bank-side movement of funds into the pool.
func (k Keeper) MintShares(
	ctx sdk.Context, consumerId uint64, depositor sdk.AccAddress, amount sdk.Coin,
) error {
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	balance := k.bankKeeper.GetBalance(ctx, poolAddr, amount.Denom)

	totalKey := collections.Join(consumerId, amount.Denom)
	total, err := k.ConsumerFeePoolTotalShares.Get(ctx, totalKey)
	if err != nil {
		total = math.ZeroInt()
	}

	// Lazy invalidation: balance == 0 with leftover shares means everyone's
	// claim is zero. Clear stale shares before treating as initial deposit.
	if balance.Amount.IsZero() && total.IsPositive() {
		if err := k.clearAllShares(ctx, consumerId, amount.Denom); err != nil {
			return err
		}
		total = math.ZeroInt()
	}

	// balance > 0 with no shares should be unreachable: every code path that
	// adds balance also mints shares, and the send-restriction blocks any
	// other deposit. InitGenesis catches the same condition on import. If we
	// see it here mid-life, the state machine is corrupted.
	if total.IsZero() && balance.Amount.IsPositive() {
		panic(fmt.Sprintf(
			"fee-pool invariant violated: consumer %d denom %s has balance %s but no shares",
			consumerId, amount.Denom, balance.Amount.String(),
		))
	}

	var shares math.Int
	if total.IsZero() {
		shares = amount.Amount
	} else {
		// shares_to_mint = amount * total / balance
		// (balance is balance BEFORE this deposit lands)
		shares = amount.Amount.Mul(total).Quo(balance.Amount)
	}
	if !shares.IsPositive() {
		// sub-share deposit (extreme dilution) — should be very rare but
		// refuse rather than silently dropping
		return errorsmod.Wrap(types.ErrDepositTooSmall,
			"deposit too small to mint any shares")
	}

	depKey := collections.Join3(consumerId, amount.Denom, depositor)
	existing, err := k.ConsumerFeePoolShares.Get(ctx, depKey)
	if err != nil {
		existing = math.ZeroInt()
	}
	if err := k.ConsumerFeePoolShares.Set(ctx, depKey, existing.Add(shares)); err != nil {
		return err
	}
	return k.ConsumerFeePoolTotalShares.Set(ctx, totalKey, total.Add(shares))
}

// WithdrawShares burns shares for the given depositor and returns the tokens
// to send. If amount >= claim, burns all of the depositor's shares (full
// branch). Otherwise burns a proportional amount (partial branch), truncated
// downward so the depositor never over-withdraws. Caller is responsible for
// dispatching the bank send.
func (k Keeper) WithdrawShares(
	ctx sdk.Context, consumerId uint64, depositor sdk.AccAddress, amount sdk.Coin,
) (sdk.Coin, error) {
	depKey := collections.Join3(consumerId, amount.Denom, depositor)
	shares, err := k.ConsumerFeePoolShares.Get(ctx, depKey)
	if err != nil {
		return sdk.Coin{}, errorsmod.Wrapf(types.ErrNoSharesForDepositor,
			"depositor %s has no shares in (%d, %s)", depositor, consumerId, amount.Denom)
	}
	totalKey := collections.Join(consumerId, amount.Denom)
	total, err := k.ConsumerFeePoolTotalShares.Get(ctx, totalKey)
	if err != nil || total.IsZero() {
		return sdk.Coin{}, errorsmod.Wrap(types.ErrPoolEmpty,
			"no shares accounted for this denom")
	}

	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	balance := k.bankKeeper.GetBalance(ctx, poolAddr, amount.Denom)
	if balance.Amount.IsZero() {
		return sdk.Coin{}, errorsmod.Wrapf(types.ErrPoolEmpty,
			"pool has zero balance for denom %s", amount.Denom)
	}

	claim := shares.Mul(balance.Amount).Quo(total)

	var sharesToBurn, tokensToSend math.Int
	if amount.Amount.GTE(claim) {
		// Full branch: burn all shares, deliver exact claim
		sharesToBurn = shares
		tokensToSend = claim
	} else {
		// Partial branch: shares_to_burn = floor(amount * total / balance)
		sharesToBurn = amount.Amount.Mul(total).Quo(balance.Amount)
		if sharesToBurn.IsZero() {
			return sdk.Coin{}, errorsmod.Wrapf(types.ErrSubShareWithdraw,
				"requested %s but pool is too diluted to burn any shares",
				amount.String())
		}
		tokensToSend = sharesToBurn.Mul(balance.Amount).Quo(total)
	}

	remainingShares := shares.Sub(sharesToBurn)
	if remainingShares.IsZero() {
		if err := k.ConsumerFeePoolShares.Remove(ctx, depKey); err != nil {
			return sdk.Coin{}, err
		}
	} else {
		if err := k.ConsumerFeePoolShares.Set(ctx, depKey, remainingShares); err != nil {
			return sdk.Coin{}, err
		}
	}

	newTotal := total.Sub(sharesToBurn)
	if newTotal.IsZero() {
		if err := k.ConsumerFeePoolTotalShares.Remove(ctx, totalKey); err != nil {
			return sdk.Coin{}, err
		}
	} else {
		if err := k.ConsumerFeePoolTotalShares.Set(ctx, totalKey, newTotal); err != nil {
			return sdk.Coin{}, err
		}
	}

	return sdk.NewCoin(amount.Denom, tokensToSend), nil
}

// SweepConsumerFeePoolDenom drains the consumer's pool for the given denom,
// distributing pro-rata to all share-holders and routing the truncation
// residue to the community pool. Share records and total for the
// (consumer, denom) pair are deleted.
//
// Distribution to the distribution module account uses FundCommunityPool
// rather than a raw bank send, so the community pool's FeePool DecCoins are
// credited correctly.
//
// This function does not return an error: under valid state it cannot fail.
// The pool balance is moved into the provider module in one hop and then
// distributed back out, so the module always holds exactly enough; depositors
// are tx signers (never blocked module accounts) so the per-depositor sends
// cannot be rejected; and the distribution-module depositor is paid via
// FundCommunityPool, not a (blocked) module send. Any remaining error path is
// a collections store/codec failure or a bank/distribution rejection that can
// only arise from state corruption or app misconfiguration -- in those cases
// we panic rather than return, so deletion can never be silently aborted and
// leave a consumer stranded in STOPPED.
func (k Keeper) SweepConsumerFeePoolDenom(
	ctx sdk.Context, consumerId uint64, denom string,
) {
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	balance := k.bankKeeper.GetBalance(ctx, poolAddr, denom)
	totalKey := collections.Join(consumerId, denom)
	total, err := k.ConsumerFeePoolTotalShares.Get(ctx, totalKey)
	if err != nil {
		total = math.ZeroInt()
	}

	if balance.Amount.IsZero() && total.IsZero() {
		return
	}

	// Orphan balance: balance > 0 with no shares. Unreachable from valid ops
	// (see MintShares; InitGenesis catches the import case), so this is a
	// state-corruption signal.
	if total.IsZero() {
		panic(fmt.Sprintf(
			"fee-pool invariant violated: consumer %d denom %s has balance %s but no shares",
			consumerId, denom, balance.Amount.String(),
		))
	}

	// Orphan shares: shares > 0 but balance == 0. Burn all shares, no transfer.
	if balance.Amount.IsZero() {
		if err := k.clearAllShares(ctx, consumerId, denom); err != nil {
			panic(fmt.Sprintf("fee-pool sweep: clear shares for consumer %d denom %s: %s",
				consumerId, denom, err))
		}
		k.emitSweepEvent(ctx, consumerId, denom, math.ZeroInt(), math.ZeroInt())
		return
	}

	providerModule := types.ModuleName
	providerAddr := authtypes.NewModuleAddress(providerModule)
	distrAddr := authtypes.NewModuleAddress(disttypes.ModuleName)

	// Move full balance into provider module account in one hop.
	if err := k.bankKeeper.SendCoinsFromAccountToModule(
		ctx, poolAddr, providerModule, sdk.NewCoins(balance),
	); err != nil {
		panic(fmt.Sprintf("fee-pool sweep: drain pool %s for consumer %d: %s",
			poolAddr, consumerId, err))
	}

	// One pass: iterate share records, distribute each slice, then clear
	// records in a final clearAllShares (which buffers keys before deleting
	// so the in-flight iterator is not invalidated).
	distributed := math.ZeroInt()
	prefix := collections.NewSuperPrefixedTripleRange[uint64, string, sdk.AccAddress](consumerId, denom)
	iter, err := k.ConsumerFeePoolShares.Iterate(ctx, prefix)
	if err != nil {
		panic(fmt.Sprintf("fee-pool sweep: iterate shares for consumer %d denom %s: %s",
			consumerId, denom, err))
	}
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			iter.Close()
			panic(fmt.Sprintf("fee-pool sweep: read share key: %s", err))
		}
		shares, err := iter.Value()
		if err != nil {
			iter.Close()
			panic(fmt.Sprintf("fee-pool sweep: read share value: %s", err))
		}
		slice := shares.Mul(balance.Amount).Quo(total)
		if slice.IsZero() {
			continue
		}
		coins := sdk.NewCoins(sdk.NewCoin(denom, slice))
		addr := key.K3()
		if addr.Equals(distrAddr) {
			if err := k.distributionKeeper.FundCommunityPool(ctx, coins, providerAddr); err != nil {
				iter.Close()
				panic(fmt.Sprintf("fee-pool sweep: fund community pool for consumer %d denom %s: %s",
					consumerId, denom, err))
			}
		} else {
			if err := k.bankKeeper.SendCoinsFromModuleToAccount(
				ctx, providerModule, addr, coins,
			); err != nil {
				iter.Close()
				panic(fmt.Sprintf("fee-pool sweep: pay depositor %s for consumer %d denom %s: %s",
					addr, consumerId, denom, err))
			}
		}
		distributed = distributed.Add(slice)
	}
	iter.Close()

	// Truncation residue -> community pool.
	dust := balance.Amount.Sub(distributed)
	if dust.IsPositive() {
		if err := k.distributionKeeper.FundCommunityPool(
			ctx, sdk.NewCoins(sdk.NewCoin(denom, dust)), providerAddr,
		); err != nil {
			panic(fmt.Sprintf("fee-pool sweep: fund community pool dust for consumer %d denom %s: %s",
				consumerId, denom, err))
		}
	}

	if err := k.clearAllShares(ctx, consumerId, denom); err != nil {
		panic(fmt.Sprintf("fee-pool sweep: clear shares for consumer %d denom %s: %s",
			consumerId, denom, err))
	}
	k.emitSweepEvent(ctx, consumerId, denom, distributed, dust)
}

// SweepConsumerFeePool sweeps each denom in `denoms`, or every denom that
// has either non-zero shares or non-zero pool balance if `denoms` is nil/empty.
// Like SweepConsumerFeePoolDenom it does not return an error: it either
// succeeds or panics on state corruption (see that function's doc).
func (k Keeper) SweepConsumerFeePool(
	ctx sdk.Context, consumerId uint64, denoms []string,
) {
	if len(denoms) > 0 {
		for _, d := range denoms {
			k.SweepConsumerFeePoolDenom(ctx, consumerId, d)
		}
		return
	}

	// Union of denoms-with-shares and denoms-with-balance.
	set := map[string]struct{}{}
	prefix := collections.NewPrefixedPairRange[uint64, string](consumerId)
	iter, err := k.ConsumerFeePoolTotalShares.Iterate(ctx, prefix)
	if err != nil {
		panic(fmt.Sprintf("fee-pool sweep: iterate totals for consumer %d: %s", consumerId, err))
	}
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			iter.Close()
			panic(fmt.Sprintf("fee-pool sweep: read total key for consumer %d: %s", consumerId, err))
		}
		set[key.K2()] = struct{}{}
	}
	iter.Close()

	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	for _, c := range k.bankKeeper.GetAllBalances(ctx, poolAddr) {
		set[c.Denom] = struct{}{}
	}

	// Deterministic iteration.
	keys := make([]string, 0, len(set))
	for d := range set {
		keys = append(keys, d)
	}
	sort.Strings(keys)
	for _, d := range keys {
		k.SweepConsumerFeePoolDenom(ctx, consumerId, d)
	}
}

// clearAllShares deletes every share record for the given (consumer, denom)
// and the matching total_shares entry. Used by lazy invalidation and by
// sweep finalization.
func (k Keeper) clearAllShares(ctx sdk.Context, consumerId uint64, denom string) error {
	if err := k.ConsumerFeePoolShares.Clear(ctx,
		collections.NewSuperPrefixedTripleRange[uint64, string, sdk.AccAddress](consumerId, denom),
	); err != nil {
		return err
	}
	totalKey := collections.Join(consumerId, denom)
	if has, err := k.ConsumerFeePoolTotalShares.Has(ctx, totalKey); err != nil {
		return err
	} else if !has {
		return nil
	}
	return k.ConsumerFeePoolTotalShares.Remove(ctx, totalKey)
}
