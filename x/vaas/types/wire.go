package types

import (
	abci "github.com/cometbft/cometbft/abci/types"

	errorsmod "cosmossdk.io/errors"
)

func NewValidatorSetChangePacketData(valUpdates []abci.ValidatorUpdate, valUpdateID uint64) ValidatorSetChangePacketData {
	return ValidatorSetChangePacketData{
		ValidatorUpdates: valUpdates,
		ValsetUpdateId:   valUpdateID,
	}
}

// Validate is used for validating the VAAS packet data.
func (vsc ValidatorSetChangePacketData) Validate() error {
	// A VSC packet is emitted once per epoch per launched consumer and
	// carries the current ConsumerInDebt flag. ValidatorUpdates may be empty
	// when the validator set did not change during the epoch; nil and empty
	// slices are treated equivalently here.
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
