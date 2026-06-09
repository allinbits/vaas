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
// as per the AtomOne constitution, and is opt-in for consumer chains.
// While the remainder of the VAAS implementation is built trying to make
// it as agnostic as possible with respect to what is the provider chain,
// this particular decorator is tightly coupled to the assumption that the
// provider is AtomOne.

// PhotonBaseDenom is the AtomOne base (micro)denomination that is bridged to
// consumers as the photon fee voucher.
const PhotonBaseDenom = "uphoton"

// PhotonFeeKeeper is the narrow consumer-keeper dependency: it resolves the
// provider (AtomOne) IBC client the photon voucher denom is anchored to.
type PhotonFeeKeeper interface {
	GetProviderClientID(ctx context.Context) (string, bool)
}

// PhotonFeeDecorator rejects transactions whose fees are not denominated in the
// one-hop photon voucher received directly from AtomOne over the provider client.
//
// It is opt-in: to use it a consumer wires it into its ante chain,
// immediately before ante.NewDeductFeeDecorator. Until the provider client is
// established (pre-VAAS) it is a no-op.
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
	providerClientID, ok := d.keeper.GetProviderClientID(ctx)
	if !ok {
		// Pre-VAAS: provider client not established, so no photon voucher can exist
		// yet. Stay out of the way so the IBC stack can be set up.
		return next(ctx, tx, simulate)
	}

	feeTx, ok := tx.(sdk.FeeTx)
	if !ok {
		return next(ctx, tx, simulate)
	}

	expected := ExpectedPhotonDenom(providerClientID)
	for _, coin := range feeTx.GetFee() {
		if coin.Denom != expected {
			return ctx, errorsmod.Wrapf(
				consumertypes.ErrInvalidFeeDenom,
				"fee denom %s is not the photon denom %s", coin.Denom, expected,
			)
		}
	}

	return next(ctx, tx, simulate)
}
