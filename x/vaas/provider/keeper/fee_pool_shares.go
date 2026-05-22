package keeper

import (
	"sort"

	"github.com/allinbits/vaas/x/vaas/provider/types"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	disttypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
)

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
// If state corruption ever leaves balance > 0 with total_shares == 0
// (invariant violation, unreachable from valid ops), the orphan balance is
// forwarded to the community pool before the new deposit is credited, so the
// new depositor cannot capture it.
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

	// Defensive: balance > 0 with no shares is unreachable from valid ops.
	// If it ever occurs (state corruption, external manipulation), forward
	// the orphan balance to the community pool before crediting the new
	// depositor, so the deposit doesn't capture the orphan funds.
	if total.IsZero() && balance.Amount.IsPositive() {
		providerAddr := authtypes.NewModuleAddress(types.ModuleName)
		orphan := sdk.NewCoins(balance)
		if err := k.bankKeeper.SendCoinsFromAccountToModule(
			ctx, poolAddr, types.ModuleName, orphan,
		); err != nil {
			return err
		}
		if err := k.distributionKeeper.FundCommunityPool(ctx, orphan, providerAddr); err != nil {
			return err
		}
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
		return sdk.Coin{}, errorsmod.Wrapf(types.ErrUnauthorized,
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
// Handles the orphan-balance defensive case: if total_shares == 0 but
// balance > 0, forwards the balance to the community pool.
func (k Keeper) SweepConsumerFeePoolDenom(
	ctx sdk.Context, consumerId uint64, denom string,
) error {
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	balance := k.bankKeeper.GetBalance(ctx, poolAddr, denom)
	totalKey := collections.Join(consumerId, denom)
	total, err := k.ConsumerFeePoolTotalShares.Get(ctx, totalKey)
	if err != nil {
		total = math.ZeroInt()
	}

	// Trivial cases
	if balance.Amount.IsZero() && total.IsZero() {
		return nil
	}

	providerModule := types.ModuleName
	providerAddr := authtypes.NewModuleAddress(providerModule)
	distrAddr := authtypes.NewModuleAddress(disttypes.ModuleName)

	// Orphan balance: no shares but balance > 0. Forward to community pool.
	if total.IsZero() {
		if err := k.bankKeeper.SendCoinsFromAccountToModule(
			ctx, poolAddr, providerModule, sdk.NewCoins(balance),
		); err != nil {
			return err
		}
		return k.distributionKeeper.FundCommunityPool(ctx, sdk.NewCoins(balance), providerAddr)
	}

	// Orphan shares: shares > 0 but balance == 0. Burn all shares, no transfer.
	if balance.Amount.IsZero() {
		return k.clearAllShares(ctx, consumerId, denom)
	}

	// Normal pro-rata distribution.
	// Move full balance into provider module account in one hop.
	if err := k.bankKeeper.SendCoinsFromAccountToModule(
		ctx, poolAddr, providerModule, sdk.NewCoins(balance),
	); err != nil {
		return err
	}

	// Collect share-holders in deterministic order (by address bytes).
	type holder struct {
		addr   sdk.AccAddress
		shares math.Int
	}
	var holders []holder
	prefix := collections.NewSuperPrefixedTripleRange[uint64, string, sdk.AccAddress](consumerId, denom)
	iter, err := k.ConsumerFeePoolShares.Iterate(ctx, prefix)
	if err != nil {
		return err
	}
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			iter.Close()
			return err
		}
		v, err := iter.Value()
		if err != nil {
			iter.Close()
			return err
		}
		holders = append(holders, holder{addr: key.K3(), shares: v})
	}
	iter.Close()

	// Distribute pro-rata.
	distributed := math.ZeroInt()
	for _, h := range holders {
		slice := h.shares.Mul(balance.Amount).Quo(total)
		if slice.IsZero() {
			continue
		}
		coins := sdk.NewCoins(sdk.NewCoin(denom, slice))
		if h.addr.Equals(distrAddr) {
			if err := k.distributionKeeper.FundCommunityPool(ctx, coins, providerAddr); err != nil {
				return err
			}
		} else {
			if err := k.bankKeeper.SendCoinsFromModuleToAccount(
				ctx, providerModule, h.addr, coins,
			); err != nil {
				return err
			}
		}
		distributed = distributed.Add(slice)
	}

	// Truncation residue → community pool.
	dust := balance.Amount.Sub(distributed)
	if dust.IsPositive() {
		if err := k.distributionKeeper.FundCommunityPool(
			ctx, sdk.NewCoins(sdk.NewCoin(denom, dust)), providerAddr,
		); err != nil {
			return err
		}
	}

	// Burn all share records for this (consumer, denom).
	return k.clearAllShares(ctx, consumerId, denom)
}

// SweepConsumerFeePool sweeps each denom in `denoms`, or every denom that
// has either non-zero shares or non-zero pool balance if `denoms` is nil/empty.
func (k Keeper) SweepConsumerFeePool(
	ctx sdk.Context, consumerId uint64, denoms []string,
) error {
	if len(denoms) > 0 {
		for _, d := range denoms {
			if err := k.SweepConsumerFeePoolDenom(ctx, consumerId, d); err != nil {
				return err
			}
		}
		return nil
	}

	// Union of denoms-with-shares and denoms-with-balance.
	set := map[string]struct{}{}
	prefix := collections.NewPrefixedPairRange[uint64, string](consumerId)
	iter, err := k.ConsumerFeePoolTotalShares.Iterate(ctx, prefix)
	if err != nil {
		return err
	}
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			iter.Close()
			return err
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
		if err := k.SweepConsumerFeePoolDenom(ctx, consumerId, d); err != nil {
			return err
		}
	}
	return nil
}

// clearAllShares deletes every share record for the given (consumer, denom).
// Used by lazy invalidation and by sweep finalization.
func (k Keeper) clearAllShares(ctx sdk.Context, consumerId uint64, denom string) error {
	prefix := collections.NewSuperPrefixedTripleRange[uint64, string, sdk.AccAddress](consumerId, denom)
	iter, err := k.ConsumerFeePoolShares.Iterate(ctx, prefix)
	if err != nil {
		return err
	}
	defer iter.Close()

	var toDelete []collections.Triple[uint64, string, sdk.AccAddress]
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			return err
		}
		toDelete = append(toDelete, key)
	}
	for _, key := range toDelete {
		if err := k.ConsumerFeePoolShares.Remove(ctx, key); err != nil {
			return err
		}
	}
	return k.ConsumerFeePoolTotalShares.Remove(ctx, collections.Join(consumerId, denom))
}
