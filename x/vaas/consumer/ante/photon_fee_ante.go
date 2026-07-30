package ante

import (
	"context"

	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"

	transfertypes "github.com/cosmos/ibc-go/v10/modules/apps/transfer/types"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
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
// -- the decorator is a full no-op. A chain opts in by setting the param in its
// consumer genesis, or later through a governance MsgUpdateParams.
//
// The switch belongs in the module params, i.e. in consensus state, rather than
// in node configuration: the decorator has no CheckTx-only carve-out and so
// also runs in FinalizeBlock, where nodes reading the switch from their own
// config files would disagree on whether a transaction is valid and diverge on
// the app hash.
//
// The expected voucher denom is derived, per transaction, from the currently
// pinned provider client id (see ExpectedPhotonDenom). That derivation is
// deliberate: the consumer pins its provider client at genesis to a client it
// created itself, which can never carry packets, and moves the pin exactly
// once -- to the first relayer-created client that delivers a VSC packet --
// after which the pin never moves again (see enforcePinnedProviderClient in
// x/vaas/consumer/keeper/relay.go). ICS-20 vouchers can only ever arrive over
// that adopted, routable client, so the denom derived from the live pin is
// exactly the denom real photon vouchers carry. A static genesis-time
// parameter could not provide this: the adopted client id is not knowable at
// genesis (it depends on which relayer client delivers the first VSC), so any
// pre-declared denom would name a voucher that can never exist.
//
// The decorator runs in two phases, split at the moment the pinned provider
// client becomes routable:
//
//   - bootstrap (no pin, or the pin still rests on the unroutable genesis
//     client): a no-op. No photon voucher can exist yet -- vouchers only
//     travel over a routable client -- so there is nothing for a fee policy
//     to protect, while rejecting fee-less transactions here would block the
//     very relayer traffic that delivers the first VSC and moves the pin.
//     This mirrors MsgFilterDecorator's pre-client behavior of staying out of
//     the way of IBC bootstrap (that decorator additionally restricts
//     pre-client traffic to IBC messages when wired, as the reference
//     consumer app does).
//
//   - enforcing (the pin is routable, so vouchers can exist): every fee coin
//     must be the derived voucher denom, and the fee must not be empty --
//     paying nothing is not paying in photon, and waving empty fees through
//     would reduce the photon-only policy to a suggestion. Gas-estimation
//     simulations are exempt from the non-empty requirement, since fees are
//     typically computed only after simulating.
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
