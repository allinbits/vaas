package ibcshim

import (
	"context"

	transfertypes "github.com/cosmos/ibc-go/v10/modules/apps/transfer/types"

	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

// TransferBankKeeper is the bank keeper the ICS-20 transfer module writes
// voucher metadata through. ibc-go's transfer keeper records a received
// voucher's metadata with the trace's base denom (`uphoton`) as its only
// denomination unit while the metadata's base is the voucher denom
// (`ibc/<hash>`), and with the full trace path as the display denom
// (ibc-go v10.5, modules/apps/transfer/keeper/keeper.go, SetDenomMetadata).
// The bank module's Metadata.Validate requires the first unit to be the base
// and the display to be a unit, so a chain that has received one voucher
// exports a genesis that `genesis validate` refuses, with the node itself
// none the wiser (InitGenesis does not validate metadata). The same defect
// is worked around at the validation end by celestiaorg/cosmos-sdk#176; here
// it is corrected at the source, so the stored metadata is valid from the
// first voucher.
//
// Every other method is the wrapped keeper's. Delete this once ibc-go writes
// metadata the bank module accepts.
type TransferBankKeeper struct {
	transfertypes.BankKeeper
}

// NewTransferBankKeeper wraps the bank keeper the transfer module is given.
func NewTransferBankKeeper(k transfertypes.BankKeeper) TransferBankKeeper {
	return TransferBankKeeper{BankKeeper: k}
}

// SetDenomMetaData stores metadata the bank module's validation accepts: the
// base denom becomes the one unit and the display denom. ibc-go's description,
// name and symbol, which carry the trace path and the base denom, are kept.
// Metadata that already validates is stored as given.
func (k TransferBankKeeper) SetDenomMetaData(ctx context.Context, metadata banktypes.Metadata) {
	k.BankKeeper.SetDenomMetaData(ctx, ValidVoucherMetadata(metadata))
}

// ValidVoucherMetadata returns metadata as ibc-go's transfer keeper writes it,
// rewritten so that Metadata.Validate accepts it, or metadata itself when it
// already does.
func ValidVoucherMetadata(metadata banktypes.Metadata) banktypes.Metadata {
	if metadata.Validate() == nil {
		return metadata
	}
	fixed := metadata
	fixed.DenomUnits = []*banktypes.DenomUnit{{Denom: metadata.Base, Exponent: 0}}
	fixed.Display = metadata.Base
	return fixed
}
