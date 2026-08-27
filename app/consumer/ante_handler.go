package app

import (
	errorsmod "cosmossdk.io/errors"
	consumerante "github.com/allinbits/vaas/x/vaas/consumer/ante"
	ibcconsumerkeeper "github.com/allinbits/vaas/x/vaas/consumer/keeper"
	ibcante "github.com/cosmos/ibc-go/v10/modules/core/ante"
	ibckeeper "github.com/cosmos/ibc-go/v10/modules/core/keeper"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
)

// HandlerOptions extend the SDK's AnteHandler options by requiring the IBC
// channel keeper.
type HandlerOptions struct {
	ante.HandlerOptions

	IBCKeeper      *ibckeeper.Keeper
	ConsumerKeeper ibcconsumerkeeper.Keeper
}

func NewAnteHandler(options HandlerOptions) (sdk.AnteHandler, error) {
	if options.AccountKeeper == nil {
		return nil, errorsmod.Wrap(sdkerrors.ErrLogic, "account keeper is required for AnteHandler")
	}
	if options.BankKeeper == nil {
		return nil, errorsmod.Wrap(sdkerrors.ErrLogic, "bank keeper is required for AnteHandler")
	}
	if options.SignModeHandler == nil {
		return nil, errorsmod.Wrap(sdkerrors.ErrLogic, "sign mode handler is required for ante builder")
	}

	sigGasConsumer := options.SigGasConsumer
	if sigGasConsumer == nil {
		sigGasConsumer = ante.DefaultSigVerificationGasConsumer
	}

	anteDecorators := []sdk.AnteDecorator{
		ante.NewSetUpContextDecorator(),
		ante.NewExtensionOptionsDecorator(nil),
		consumerante.NewDisabledModulesDecorator("/cosmos.evidence", "/cosmos.slashing"),
		ante.NewValidateBasicDecorator(),
		consumerante.NewMsgFilterDecorator(options.ConsumerKeeper),
		ante.NewTxTimeoutHeightDecorator(),
		ante.NewValidateMemoDecorator(options.AccountKeeper),
		ante.NewConsumeGasForTxSizeDecorator(options.AccountKeeper),
		// The photon fee policy sits immediately before fee deduction, per the
		// decorator's contract: it vets the fee denom that DeductFeeDecorator
		// is about to collect. It self-gates on the photon_fees_enabled
		// consumer param, so it is wired unconditionally and no-ops on chains
		// that did not opt in.
		consumerante.NewPhotonFeeDecorator(options.ConsumerKeeper),
		// The infrastructure exemption must hold at the node fee floor too:
		// with the SDK's default checker, a raised minimum-gas-prices would
		// price the relayer traffic the photon exemption keeps flowing. An
		// explicitly configured checker still wins.
		ante.NewDeductFeeDecorator(options.AccountKeeper, options.BankKeeper, options.FeegrantKeeper, txFeeCheckerOrDefault(options.TxFeeChecker)),
		// SetPubKeyDecorator must be called before all signature verification decorators
		ante.NewSetPubKeyDecorator(options.AccountKeeper),
		ante.NewValidateSigCountDecorator(options.AccountKeeper),
		ante.NewSigGasConsumeDecorator(options.AccountKeeper, sigGasConsumer),
		ante.NewSigVerificationDecorator(options.AccountKeeper, options.SignModeHandler),
		ante.NewIncrementSequenceDecorator(options.AccountKeeper),
		ibcante.NewRedundantRelayDecorator(options.IBCKeeper),
	}

	return sdk.ChainAnteDecorators(anteDecorators...), nil
}

// txFeeCheckerOrDefault returns the explicitly configured checker, or the
// consumer's infrastructure-exempt one when none is set.
func txFeeCheckerOrDefault(checker ante.TxFeeChecker) ante.TxFeeChecker {
	if checker != nil {
		return checker
	}
	return consumerante.InfrastructureExemptTxFeeChecker
}
