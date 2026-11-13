package keeper

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	disttypes "github.com/cosmos/cosmos-sdk/x/distribution/types"

	"github.com/allinbits/vaas/x/vaas/provider/types"
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

// Bounds on a pool's share total. Epoch distributions draw tokens from a pool
// without burning shares, so every deposit onto a drained pool multiplies the
// shares-per-token ratio, and nothing else bounds it. A mint that would take
// the total past shareTotalRebaseThreshold rescales the ledger first (see
// rebaseShares); one that would still take it past shareTotalLimit is refused.
// Together they keep every stored total, and every product the claim math
// forms from it, far inside the 256 bits math.Int can hold.
var (
	shareTotalRebaseThreshold = new(big.Int).Lsh(big.NewInt(1), 128)
	shareTotalLimit           = new(big.Int).Lsh(big.NewInt(1), 200)
)

// rebaseSharesPerTokenBits is the number of shares per token a rebase leaves
// in the ledger at the least, as a power of two: 2^32.
const rebaseSharesPerTokenBits = 32

// mulDiv returns floor(a * b / c) computed over big.Int, so the product may
// pass the 256-bit bound that math.Int enforces by panicking. c must be
// positive.
func mulDiv(a, b, c math.Int) *big.Int {
	p := new(big.Int).Mul(a.BigInt(), b.BigInt())
	return p.Quo(p, c.BigInt())
}

// feePoolDraw resolves what a depositor holding `shares` of `total` gets out of
// a pool holding `balance` when at most `limit` tokens may leave the pool right
// now: `limit` is the lesser of what was asked for and the balance not reserved
// as withheld-fee escrow (see outstandingWithheldFees), so it never passes the
// balance.
//
// Shares are always measured against the full pool balance -- they back the
// escrowed portion too -- so a draw held back by `limit` burns shares at the
// full-balance rate and its token amount is re-derived from the shares actually
// burned. The remainder therefore stays backed by the depositor's residual
// shares, and the pool is never left holding a balance with no shares. Both
// results are zero when nothing can be drawn.
//
// Every product is taken over big.Int (see mulDiv): the share total may sit far
// above the balance, and math.Int panics past 256 bits. Each quotient is
// bounded by one of its operands, a token amount by the balance and a share
// amount by the total, so the results always convert back.
//
// This is the single definition of the fee-pool claim: WithdrawShares moves
// money by it, and ComputeClaim (and through it the claim queries) quote it, so
// a quote can never promise more than a withdrawal delivers.
func feePoolDraw(shares, total, balance, limit math.Int) (sharesToBurn, tokens math.Int) {
	if !shares.IsPositive() || !total.IsPositive() ||
		!balance.IsPositive() || !limit.IsPositive() {
		return math.ZeroInt(), math.ZeroInt()
	}
	claim := math.NewIntFromBigInt(mulDiv(shares, balance, total))
	if claim.LTE(limit) {
		// The whole claim fits: burn every share and deliver it.
		return shares, claim
	}
	sharesToBurn = math.NewIntFromBigInt(mulDiv(limit, total, balance))
	if !sharesToBurn.IsPositive() {
		return math.ZeroInt(), math.ZeroInt()
	}
	return sharesToBurn, math.NewIntFromBigInt(mulDiv(sharesToBurn, balance, total))
}

// ComputeClaim returns the depositor's currently claimable tokens for one denom
// on the given consumer's fee pool: exactly what a withdrawal of the whole
// claim would deliver at this block. Returns math.ZeroInt() if the depositor has
// no shares, total_shares is zero, or the entire pool balance is reserved as
// withheld-fee escrow -- the escrowed portion backs a pending downtime
// challenge's make-whole payout and cannot be drawn until the challenge resolves
// (see outstandingWithheldFees and WithdrawShares). Shares blocked by the
// escrow are not lost: they keep backing the balance and become claimable once
// the escrow clears.
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
		// Absent means no claim; anything else is store corruption and must
		// not be misread as a zero claim.
		if !errors.Is(err, collections.ErrNotFound) {
			panic(fmt.Errorf("failed to read fee pool shares for consumer %d denom %s: %w", consumerId, denom, err))
		}
		return math.ZeroInt()
	}
	total, err := k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
	if err != nil {
		if !errors.Is(err, collections.ErrNotFound) {
			panic(fmt.Errorf("failed to read fee pool total shares for consumer %d denom %s: %w", consumerId, denom, err))
		}
		return math.ZeroInt()
	}
	if total.IsZero() {
		return math.ZeroInt()
	}
	// Nothing bounds the request, so the unreserved balance is the only limit.
	available := balance.Amount.Sub(k.outstandingWithheldFees(ctx, consumerId, denom))
	_, tokens := feePoolDraw(shares, total, balance.Amount, available)
	return tokens
}

// absorbUnaccountedBalance credits the distribution module account with shares
// covering a pool balance that no shares account for, so that share accounting
// covers the whole balance again. On return, total_shares for the pair equals
// `balance`.
//
// On a running chain one sender can produce such a balance, and deliberately so:
// the bank send restriction rejects every direct transfer into a pool address
// except from the provider module -- which always mints shares alongside -- and
// from the distribution module, so that community-pool spends can reach a pool at
// all (see FeePoolSendRestriction). A community-pool spend addressed straight at
// a pool address therefore lands funds in it without minting anything. Crediting
// the distribution module account is the same accounting a gov-signed
// MsgFundConsumerFeePool would have produced for those funds: the balance
// returns to the community pool when the pool is swept, and governance can
// withdraw it before then. Any share record left without a stored total backs no
// claim at all (every reader treats a missing total as an empty pool), so it is
// cleared rather than silently re-valued by the new total.
//
// This is logged at error level: nothing about it is routine, and an operator
// wants to know that funds reached a pool outside the accounted path.
func (k Keeper) absorbUnaccountedBalance(
	ctx sdk.Context, consumerId uint64, denom string, balance math.Int,
) error {
	k.Logger(ctx).Error(
		"consumer fee pool holds a balance no shares account for; crediting the community pool",
		"consumerId", consumerId,
		"denom", denom,
		"balance", balance.String(),
	)
	if err := k.clearAllShares(ctx, consumerId, denom); err != nil {
		return err
	}
	distrAddr := authtypes.NewModuleAddress(disttypes.ModuleName)
	if err := k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, denom, distrAddr), balance,
	); err != nil {
		return err
	}
	return k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, denom), balance)
}

// MintShares credits the depositor with shares for the given amount in the
// specified consumer's fee pool. Handles the lazy-invalidation case:
// if balance == 0 but total_shares > 0, all existing shares for this
// (consumer, denom) are deleted first (they represent worthless claims).
//
// A deposit that would take the share total past shareTotalRebaseThreshold
// rescales the ledger first (see rebaseShares); one that would take it past
// shareTotalLimit even so is refused with ErrShareLedgerOverflow.
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
		// Absent means a first deposit; anything else must fail the tx, not
		// silently reset the pool's share ledger.
		if !errors.Is(err, collections.ErrNotFound) {
			return fmt.Errorf("failed to read fee pool total shares: %w", err)
		}
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

	// balance > 0 with no shares: funds reached the pool without minting any,
	// which only a community-pool spend addressed straight at the pool address
	// can do. Book them to the community pool first so this deposit is priced
	// against a balance that shares fully cover, instead of handing the
	// depositor funds nobody minted shares for (see absorbUnaccountedBalance).
	if total.IsZero() && balance.Amount.IsPositive() {
		if err := k.absorbUnaccountedBalance(ctx, consumerId, amount.Denom, balance.Amount); err != nil {
			return err
		}
		total = balance.Amount
	}

	// shares_to_mint = amount * total / balance, the balance being the one
	// before this deposit lands; a first deposit mints one share per token.
	// The product is taken over big.Int (see mulDiv): the share total may sit
	// far above the balance.
	minted := amount.Amount.BigInt()
	if total.IsPositive() {
		minted = mulDiv(amount.Amount, total, balance.Amount)
		if new(big.Int).Add(total.BigInt(), minted).Cmp(shareTotalRebaseThreshold) > 0 {
			rebased, err := k.rebaseShares(ctx, consumerId, amount.Denom, total, balance.Amount)
			if err != nil {
				return err
			}
			total = rebased
			minted = mulDiv(amount.Amount, total, balance.Amount)
		}
	}
	if new(big.Int).Add(total.BigInt(), minted).Cmp(shareTotalLimit) > 0 {
		return errorsmod.Wrapf(types.ErrShareLedgerOverflow,
			"minting %s shares onto a total of %s for consumer %d denom %s",
			minted, total, consumerId, amount.Denom)
	}
	if minted.Sign() <= 0 {
		// sub-share deposit (extreme dilution): refuse rather than silently
		// dropping it.
		return errorsmod.Wrap(types.ErrDepositTooSmall,
			"deposit too small to mint any shares")
	}
	shares := math.NewIntFromBigInt(minted)

	depKey := collections.Join3(consumerId, amount.Denom, depositor)
	existing, err := k.ConsumerFeePoolShares.Get(ctx, depKey)
	if err != nil {
		if !errors.Is(err, collections.ErrNotFound) {
			return fmt.Errorf("failed to read depositor's fee pool shares: %w", err)
		}
		existing = math.ZeroInt()
	}
	if err := k.ConsumerFeePoolShares.Set(ctx, depKey, existing.Add(shares)); err != nil {
		return err
	}
	return k.ConsumerFeePoolTotalShares.Set(ctx, totalKey, total.Add(shares))
}

// rebaseShares rescales the share ledger of one (consumer, denom) pair down by
// a power of two, holdings and total alike, so a share total that epoch
// distributions have driven far above the balance returns to a working range
// without moving anyone's claim. The ledger keeps at least 2^32 shares per
// token: the factor is the largest power of two that leaves
// total / balance >= 2^32, and when the ratio is already below twice that
// nothing is rescaled. Returns the total the ledger holds afterwards.
//
// Rounding: every holding is truncated to the rescaled unit and the new total
// is the sum of the truncated holdings, so the ledger invariant
// total == sum(shares) stays exact. A holder's claim, shares * balance / total,
// moves by less than holders / 2^32 of a token: each truncation drops under one
// rescaled share, and a rescaled share is worth at most 2^-32 of a token. A
// holding that truncates to nothing had a claim below that already; its record
// is removed, as a withdrawal of the last share removes it.
//
// Holders are read into memory before anything is written: they are the
// depositors of this one pool, and the store must not change under an open
// iterator.
func (k Keeper) rebaseShares(
	ctx sdk.Context, consumerId uint64, denom string, total, balance math.Int,
) (math.Int, error) {
	unit := new(big.Int).Lsh(balance.BigInt(), rebaseSharesPerTokenBits)
	ratio := new(big.Int).Quo(total.BigInt(), unit)
	if ratio.BitLen() < 2 {
		return total, nil
	}
	shift := uint(ratio.BitLen() - 1)

	type holding struct {
		addr   sdk.AccAddress
		shares math.Int
	}
	var holdings []holding
	prefix := collections.NewSuperPrefixedTripleRange[uint64, string, sdk.AccAddress](consumerId, denom)
	err := k.ConsumerFeePoolShares.Walk(ctx, prefix,
		func(key collections.Triple[uint64, string, sdk.AccAddress], shares math.Int) (bool, error) {
			holdings = append(holdings, holding{addr: key.K3(), shares: shares})
			return false, nil
		})
	if err != nil {
		return math.Int{}, fmt.Errorf("failed to walk fee pool shares for consumer %d denom %s: %w", consumerId, denom, err)
	}

	newTotal := math.ZeroInt()
	for _, h := range holdings {
		rescaled := math.NewIntFromBigInt(new(big.Int).Rsh(h.shares.BigInt(), shift))
		key := collections.Join3(consumerId, denom, h.addr)
		if rescaled.IsZero() {
			if err := k.ConsumerFeePoolShares.Remove(ctx, key); err != nil {
				return math.Int{}, err
			}
			continue
		}
		if err := k.ConsumerFeePoolShares.Set(ctx, key, rescaled); err != nil {
			return math.Int{}, err
		}
		newTotal = newTotal.Add(rescaled)
	}
	if !newTotal.IsPositive() {
		return math.Int{}, fmt.Errorf("rebasing fee pool shares for consumer %d denom %s by 2^%d left no shares", consumerId, denom, shift)
	}
	if err := k.ConsumerFeePoolTotalShares.Set(ctx, collections.Join(consumerId, denom), newTotal); err != nil {
		return math.Int{}, err
	}
	k.Logger(ctx).Info("rebased consumer fee pool shares",
		"consumerId", consumerId,
		"denom", denom,
		"shift", shift,
		"total", total.String(),
		"rebasedTotal", newTotal.String(),
	)
	return newTotal, nil
}

// WithdrawShares burns shares for the given depositor and returns the tokens
// to send. A withdrawal may only ever draw the pool balance not reserved as
// withheld-fee escrow (see outstandingWithheldFees): the escrowed portion
// backs a pending downtime challenge's make-whole payout and cannot be raced
// out before the challenge resolves. How many shares burn for how many tokens
// is decided by feePoolDraw, the same computation ComputeClaim quotes.
// Caller is responsible for dispatching the bank send.
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
	available := balance.Amount.Sub(k.outstandingWithheldFees(ctx, consumerId, amount.Denom))
	if !available.IsPositive() {
		return sdk.Coin{}, errorsmod.Wrapf(types.ErrPoolEmpty,
			"pool has no withdrawable balance for denom %s (reserved as withheld-fee escrow)", amount.Denom)
	}

	// A withdrawal draws at most the unreserved balance, however much was asked
	// for.
	limit := amount.Amount
	if limit.GT(available) {
		limit = available
	}
	sharesToBurn, tokensToSend := feePoolDraw(shares, total, balance.Amount, limit)
	if !sharesToBurn.IsPositive() {
		return sdk.Coin{}, errorsmod.Wrapf(types.ErrSubShareWithdraw,
			"requested %s but the unreserved pool is too diluted to burn any shares",
			amount.String())
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
// Unlike DistributeConsumerFees and WithdrawShares, this does not reserve
// outstanding withheld-fee escrow: a sweep runs only once a consumer has left
// LAUNCHED and PAUSED (the manual sweep is gated to STOPPED and deletion
// requires STOPPED), by which point no downtime challenge can still resolve
// -- StopAndPrepareForConsumerRemoval has cancelled every pending slash and a
// stopped consumer can neither be challenged nor paused. Any withheld-fee
// record left over is therefore dead escrow the pool no longer owes to a
// validator, so the full balance is released to share-holders here.
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
// FundCommunityPool, not a (blocked) module send. A balance no shares account
// for is not treated as invalid state either -- it is absorbed (see below) --
// because this runs from BeginBlock on consumer deletion. Any remaining error
// path is a collections store/codec failure or a bank/distribution rejection
// that can only arise from state corruption or app misconfiguration -- in those
// cases we panic rather than return, so deletion can never be silently aborted
// and leave a consumer stranded in STOPPED.
func (k Keeper) SweepConsumerFeePoolDenom(
	ctx sdk.Context, consumerId uint64, denom string,
) {
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	balance := k.bankKeeper.GetBalance(ctx, poolAddr, denom)
	totalKey := collections.Join(consumerId, denom)
	total, err := k.ConsumerFeePoolTotalShares.Get(ctx, totalKey)
	if err != nil {
		// Absent means nothing was ever minted; anything else is exactly the
		// silent-abort this function's contract forbids.
		if !errors.Is(err, collections.ErrNotFound) {
			panic(fmt.Errorf("failed to read fee pool total shares for consumer %d denom %s: %w", consumerId, denom, err))
		}
		total = math.ZeroInt()
	}

	if balance.Amount.IsZero() && total.IsZero() {
		return
	}

	// Balance > 0 with no shares: credit the community pool with shares for it
	// (see absorbUnaccountedBalance) and sweep it like any other holding, which
	// hands it back to the community pool below. This must not be fatal -- a
	// community-pool spend addressed straight at the pool address can produce
	// the state, and this sweep runs from BeginBlock on consumer deletion, where
	// a panic would halt the provider instead of failing a transaction.
	if total.IsZero() {
		if err := k.absorbUnaccountedBalance(ctx, consumerId, denom, balance.Amount); err != nil {
			panic(fmt.Sprintf("fee-pool sweep: absorb unaccounted balance for consumer %d denom %s: %s",
				consumerId, denom, err))
		}
		total = balance.Amount
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
		// Over big.Int (see mulDiv): shares * balance may pass 256 bits while
		// the slice itself never passes the balance.
		slice := math.NewIntFromBigInt(mulDiv(shares, balance.Amount, total))
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
