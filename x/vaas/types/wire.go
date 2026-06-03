package types

import (
	"encoding/json"
	"fmt"

	abci "github.com/cometbft/cometbft/abci/types"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
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

// EvidencePacketData is sent from a consumer chain to the provider chain
// to report a validator infraction (e.g., downtime) detected on the consumer.
type EvidencePacketData struct {
	ValidatorAddr    sdk.ConsAddress         `json:"validator_addr"`
	InfractionHeight int64                   `json:"infraction_height"`
	Infraction       stakingtypes.Infraction `json:"infraction"`
}

// NewEvidencePacketData creates a new EvidencePacketData.
func NewEvidencePacketData(validatorAddr sdk.ConsAddress, infractionHeight int64, infraction stakingtypes.Infraction) EvidencePacketData {
	return EvidencePacketData{
		ValidatorAddr:    validatorAddr,
		InfractionHeight: infractionHeight,
		Infraction:       infraction,
	}
}

// Validate returns an error if the EvidencePacketData is invalid.
func (spd EvidencePacketData) Validate() error {
	if len(spd.ValidatorAddr) == 0 {
		return errorsmod.Wrap(ErrInvalidPacketData, "validator address cannot be empty")
	}
	if spd.InfractionHeight <= 0 {
		return errorsmod.Wrap(ErrInvalidPacketData, "infraction height must be positive")
	}
	if spd.Infraction != stakingtypes.Infraction_INFRACTION_DOWNTIME {
		return fmt.Errorf("only DOWNTIME infractions can be sent as evidence packets, got %s", spd.Infraction)
	}
	return nil
}

// GetBytes marshals the EvidencePacketData into JSON bytes for IBC transport.
func (spd EvidencePacketData) GetBytes() []byte {
	bz, err := json.Marshal(&spd)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal EvidencePacketData: %v", err))
	}
	return bz
}
