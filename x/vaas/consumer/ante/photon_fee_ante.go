package ante

import (
	"context"

	transfertypes "github.com/cosmos/ibc-go/v10/modules/apps/transfer/types"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"

	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
)

// This file implements the PhotonFeeDecorator, an ante decorator that enforces
// that transaction fees are paid in the one-hop photon voucher received from
// atomone over the provider client. This is a requirement for "core shards"
// as per the AtomOne constitution, and is opt-in for consumer chains via the
// photon_fees_enabled consumer param.
// While the remainder of the VAAS implementation is built trying to make
// it as agnostic as possible with respect to what is the provider chain,
// this particular decorator is tightly coupled to the assumption that the
// provider is AtomOne.

// PhotonBaseDenom is the AtomOne base (micro)denomination that is bridged to
// consumers as the photon fee voucher.
const PhotonBaseDenom = "uphoton"

// PhotonFeeKeeper is the narrow consumer-keeper dependency: it reports whether
// the chain enforces photon-only fees, resolves the provider (AtomOne) IBC
// client the photon voucher denom is anchored to, and reports whether that
// client can actually carry packets yet.
type PhotonFeeKeeper interface {
	PhotonFeesEnabled(ctx context.Context) bool
	GetProviderClientID(ctx context.Context) (string, bool)
	HasRoutableProviderClient(ctx context.Context) bool
}

// PhotonFeeDecorator rejects transactions whose fees are not paid in the
// one-hop photon voucher received directly from AtomOne over the provider
// client. A consumer wires it into its ante chain unconditionally, immediately
// before ante.NewDeductFeeDecorator, and turns the policy on through the
// photon_fees_enabled consumer param: while that param is false -- its default
// -- the decorator is a full no-op. A chain opts in by setting the param in a
// hand-authored consumer genesis (the provider-authored consumer genesis
// always writes it off); after genesis only the params authority can change
// it, and the reference consumer app wires no module able to sign as that
// authority, which makes the genesis choice final there.
//
// The switch belongs in the module params, i.e. in consensus state, rather than
// in node configuration: the decorator has no CheckTx-only carve-out and so
// also runs in FinalizeBlock, where nodes reading the switch from their own
// config files would disagree on whether a transaction is valid and diverge on
// the app hash.
//
// The expected voucher denom is derived, per transaction, from the pinned
// provider client id (see ExpectedPhotonDenom). That derivation is
// deliberate: a new chain starts with no pin and no provider client at all;
// the owner seeded into the consumer params (or governance) pins the
// relayer-created client exactly once via MsgSetProviderClient, which only
// accepts an active client of the provider's chain id with a registered IBC
// v2 counterparty, and from then on the pin never moves (see
// enforcePinnedProviderClient in x/vaas/consumer/keeper/relay.go). ICS-20
// vouchers can only ever arrive over that pinned, routable client, so the
// denom derived from the live pin is exactly the denom real photon vouchers
// carry. A static genesis-time parameter could not provide this: the relayer
// creates the client after genesis, so its id is not knowable when the
// genesis is written, and any pre-declared denom would name a voucher that
// can never exist.
//
// The decorator runs in two phases, split at the moment a routable provider
// client is pinned:
//
//   - bootstrap (no pin yet, or a pin whose client has no registered
//     counterparty, which only a hand-authored restart genesis can produce
//     since MsgSetProviderClient refuses such a client): a no-op. No photon
//     voucher can exist yet -- vouchers only travel over a routable client --
//     so there is nothing for a fee policy to protect, while rejecting
//     fee-less transactions here would block the very relayer traffic that
//     creates the client the owner then pins. This mirrors
//     MsgFilterDecorator's pre-pin behavior of staying out of the way of IBC
//     bootstrap (that decorator additionally restricts pre-pin traffic to IBC
//     messages, the pin message itself and governance when wired, as the
//     reference consumer app does).
//
//   - enforcing (the pin is routable, so vouchers can exist): every fee coin
//     must be the derived voucher denom, and the fee must not be empty --
//     paying nothing is not paying in photon, and waving empty fees through
//     would reduce the photon-only policy to a suggestion. Gas-estimation
//     simulations are exempt from the non-empty requirement, since fees are
//     typically computed only after simulating.
//
//     Nothing is exempt while enforcing, the relayer's deliveries and
//     governance included: a standing exemption for infrastructure messages
//     let anyone grow the chain's state for free (creating clients, updating
//     clients of their own), at a cost no node setting could put back. The
//     vouchers the relayer pays with therefore reach it during bootstrap,
//     when its deliveries are free: the owner bridges photon to the relayer
//     (and to itself) over the freshly created client before pinning it,
//     and the relayer then runs with a gas price in the voucher denom (see
//     docs/consumer-launch-runbook.md, which also covers refilling it). The
//     node fee floor follows the same split: the reference app's
//     NewBootstrapExemptTxFeeChecker waives the floor for bootstrap traffic
//     before the pin and for nothing after it.
//
// Note the decorator constrains the fee denom, not the amount; minimum-fee
// policy stays with the fee market (validator min-gas-prices and the fee
// checker in DeductFeeDecorator).
type PhotonFeeDecorator struct {
	keeper PhotonFeeKeeper
}

func NewPhotonFeeDecorator(k PhotonFeeKeeper) PhotonFeeDecorator {
	return PhotonFeeDecorator{keeper: k}
}

// ExpectedPhotonDenom derives the IBC voucher denom for one-hop uphoton received
// over the given provider client: ibc/SHA256("transfer/<providerClientID>/uphoton").
func ExpectedPhotonDenom(providerClientID string) string {
	denom := transfertypes.Denom{
		Base:  PhotonBaseDenom,
		Trace: []transfertypes.Hop{transfertypes.NewHop(transfertypes.PortID, providerClientID)},
	}
	return denom.IBCDenom()
}

func (d PhotonFeeDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	feeTx, ok := tx.(sdk.FeeTx)
	if !ok {
		return next(ctx, tx, simulate)
	}

	if !d.keeper.PhotonFeesEnabled(ctx) {
		return next(ctx, tx, simulate)
	}

	providerClientID, ok := d.keeper.GetProviderClientID(ctx)
	if !ok || !d.keeper.HasRoutableProviderClient(ctx) {
		// Bootstrap phase: no photon voucher can exist yet, see the type godoc.
		return next(ctx, tx, simulate)
	}

	expected := ExpectedPhotonDenom(providerClientID)
	fee := feeTx.GetFee()
	if fee.IsZero() && !simulate {
		return ctx, errorsmod.Wrapf(
			consumertypes.ErrInvalidFeeDenom,
			"fee cannot be empty: fees must be paid in the photon denom %s", expected,
		)
	}
	for _, coin := range fee {
		if coin.Denom != expected {
			return ctx, errorsmod.Wrapf(
				consumertypes.ErrInvalidFeeDenom,
				"fee denom %s is not the photon denom %s", coin.Denom, expected,
			)
		}
	}

	return next(ctx, tx, simulate)
}
