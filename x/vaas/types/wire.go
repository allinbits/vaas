package types

import (
	errorsmod "cosmossdk.io/errors"

	abci "github.com/cometbft/cometbft/abci/types"
)

func NewValidatorSetChangePacketData(valUpdates []abci.ValidatorUpdate, valUpdateID uint64) ValidatorSetChangePacketData {
	return ValidatorSetChangePacketData{
		ValidatorUpdates: valUpdates,
		ValsetUpdateId:   valUpdateID,
	}
}

// Validate is used for validating the VAAS packet data.
func (vsc ValidatorSetChangePacketData) Validate() error {
	// Note that vsc.ValidatorUpdates can be empty in the case of unbonding
	// operations w/o changes in the voting power of the validators in the validator set
	if vsc.ValidatorUpdates == nil {
		return errorsmod.Wrap(ErrInvalidPacketData, "validator updates cannot be nil")
	}
	// ValsetUpdateId is strictly positive
	if vsc.ValsetUpdateId == 0 {
		return errorsmod.Wrap(ErrInvalidPacketData, "valset update id cannot be equal to zero")
	}
	return nil
}

// GetBytes marshals the ValidatorSetChangePacketData into JSON string bytes
// to be sent over the wire with IBC.
func (vsc ValidatorSetChangePacketData) GetBytes() []byte {
	valUpdateBytes := ModuleCdc.MustMarshalJSON(&vsc)
	return valUpdateBytes
}
