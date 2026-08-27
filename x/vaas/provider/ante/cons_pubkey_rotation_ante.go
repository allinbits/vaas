package ante

import (
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"

	errorsmod "cosmossdk.io/errors"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

type (
	// ProviderKeeper defines the interface required by the provider-side
	// admission gate.
	ProviderKeeper interface {
		IsConsumerConsAddrInUse(ctx sdk.Context, consAddr sdk.ConsAddress) bool
	}

	// ConsPubKeyRotationDecorator rejects a tx rotating a validator's provider
	// consensus key onto a consensus key that is already assigned as some
	// validator's consumer key.
	//
	// Such a rotation would put two validators at the same consensus address on
	// that consumer, and nothing downstream can refuse it: x/staking's own
	// uniqueness check does not see VAAS key assignments, MsgRotateConsPubKey
	// carries no proof of possession of the new key (consumer keys are public),
	// and the VAAS staking hook that observes the rotation only runs in EndBlock,
	// once the rotation is already committed.
	//
	// Admission is the last point at which the rotation can still be refused
	// without touching consensus. The check reads only committed state, so it is
	// deterministic: it reaches the same verdict in CheckTx and in DeliverTx, on
	// every node, and rejecting costs nothing beyond the offending tx.
	ConsPubKeyRotationDecorator struct {
		ProviderKeeper ProviderKeeper
	}
)

func NewConsPubKeyRotationDecorator(k ProviderKeeper) ConsPubKeyRotationDecorator {
	return ConsPubKeyRotationDecorator{
		ProviderKeeper: k,
	}
}

func (d ConsPubKeyRotationDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (newCtx sdk.Context, err error) {
	for _, msg := range tx.GetMsgs() {
		if err := d.checkMsg(ctx, msg); err != nil {
			return ctx, err
		}
	}
	return next(ctx, tx, simulate)
}

// checkMsg rejects msg if it is a consensus-key rotation onto a key already in
// use as a consumer key, recursing through authz MsgExec so that wrapping the
// rotation in a grant execution -- including nested ones -- does not bypass the
// check.
func (d ConsPubKeyRotationDecorator) checkMsg(ctx sdk.Context, msg sdk.Msg) error {
	switch m := msg.(type) {
	case *stakingtypes.MsgRotateConsPubKey:
		return d.checkRotation(ctx, m)
	case *authz.MsgExec:
		innerMsgs, err := m.GetMessages()
		if err != nil {
			// The inner messages do not decode, so no rotation can result from
			// this tx: authz rejects it when it executes.
			return nil
		}
		for _, innerMsg := range innerMsgs {
			if err := d.checkMsg(ctx, innerMsg); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d ConsPubKeyRotationDecorator) checkRotation(ctx sdk.Context, msg *stakingtypes.MsgRotateConsPubKey) error {
	if msg.NewPubkey == nil {
		return nil
	}
	pubKey, ok := msg.NewPubkey.GetCachedValue().(cryptotypes.PubKey)
	if !ok {
		// The declared key does not decode into a public key, so no rotation can
		// result from this message: x/staking's own handler rejects it.
		return nil
	}

	consAddr := sdk.ConsAddress(pubKey.Address())
	if d.ProviderKeeper.IsConsumerConsAddrInUse(ctx, consAddr) {
		return errorsmod.Wrapf(
			providertypes.ErrConsumerKeyInUse,
			"cannot rotate to consensus key %s: already assigned as a consumer key", consAddr.String(),
		)
	}
	return nil
}
