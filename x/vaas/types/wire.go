package types

import (
	"encoding/json"
	"fmt"
	"math/bits"

	abci "github.com/cometbft/cometbft/abci/types"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"

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
// to report a validator downtime infraction detected on the consumer,
// together with the missed-block bitmap over the reporting window.
type EvidencePacketData struct {
	ValidatorAddr      sdk.ConsAddress         `json:"validator_addr"`
	InfractionHeight   int64                   `json:"infraction_height"` // last height of the window
	Infraction         stakingtypes.Infraction `json:"infraction"`
	WindowStartHeight  int64                   `json:"window_start_height"`
	MissedBlocksBitmap []byte                  `json:"missed_blocks_bitmap"` // bit i => height window_start_height+i missed
	SignedBlocksWindow int64                   `json:"signed_blocks_window"`
	MinSignedPerWindow math.LegacyDec          `json:"min_signed_per_window"`
}

// NewEvidencePacketData creates a new EvidencePacketData reporting a downtime
// infraction over the window [windowStart, windowStart+span-1].
func NewEvidencePacketData(validatorAddr sdk.ConsAddress, windowStart int64, bitmap []byte, span int64, window int64, minSigned math.LegacyDec) EvidencePacketData {
	return EvidencePacketData{
		ValidatorAddr:      validatorAddr,
		InfractionHeight:   windowStart + span - 1,
		Infraction:         stakingtypes.Infraction_INFRACTION_DOWNTIME,
		WindowStartHeight:  windowStart,
		MissedBlocksBitmap: bitmap,
		SignedBlocksWindow: window,
		MinSignedPerWindow: minSigned,
	}
}

// Span returns the number of heights covered by the reporting window.
func (spd EvidencePacketData) Span() int64 {
	return spd.InfractionHeight - spd.WindowStartHeight + 1
}

// MissedCount returns the number of missed blocks recorded in the bitmap
// over [0, Span()), masking out any trailing bits beyond the span.
func (spd EvidencePacketData) MissedCount() int64 {
	span := spd.Span()
	var count int64
	for i := int64(0); i < span; i += 8 {
		byteIdx := i / 8
		if byteIdx >= int64(len(spd.MissedBlocksBitmap)) {
			break
		}
		b := spd.MissedBlocksBitmap[byteIdx]
		remaining := span - i
		if remaining < 8 {
			mask := byte(1<<uint(remaining)) - 1
			b &= mask
		}
		count += int64(bits.OnesCount8(b))
	}
	return count
}

// MaxMissed returns the number of missed blocks in a full window that
// qualifies as a downtime infraction, computed from the packet's own echoed
// window parameters: window - ceil(minSigned * window). Mirrors
// InfractionParameters.MaxMissed, but operates on the packet's echoed fields
// so the provider polices exactly the threshold the consumer claims to have
// computed against (subject to AcceptableDowntimeParams verifying those
// echoed fields are themselves acceptable).
func (spd EvidencePacketData) MaxMissed() int64 {
	w := math.LegacyNewDec(spd.SignedBlocksWindow)
	return spd.SignedBlocksWindow - spd.MinSignedPerWindow.Mul(w).Ceil().TruncateInt64()
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
	if spd.WindowStartHeight <= 0 {
		return errorsmod.Wrap(ErrInvalidPacketData, "window start height must be positive")
	}
	if spd.InfractionHeight < spd.WindowStartHeight {
		return errorsmod.Wrap(ErrInvalidPacketData, "infraction height cannot be before window start height")
	}
	if spd.SignedBlocksWindow <= 0 {
		return errorsmod.Wrap(ErrInvalidPacketData, "signed blocks window must be positive")
	}
	if spd.Span() > spd.SignedBlocksWindow {
		return errorsmod.Wrap(ErrInvalidPacketData, "window span cannot exceed signed blocks window")
	}
	wantBitmapLen := (spd.Span() + 7) / 8
	if int64(len(spd.MissedBlocksBitmap)) != wantBitmapLen {
		return errorsmod.Wrap(ErrInvalidPacketData, "missed blocks bitmap length does not match window span")
	}
	if spd.MinSignedPerWindow.LTE(math.LegacyZeroDec()) || spd.MinSignedPerWindow.GTE(math.LegacyOneDec()) {
		return errorsmod.Wrap(ErrInvalidPacketData, "min signed per window must be between 0 and 1")
	}
	if spd.MissedCount() <= 0 {
		return errorsmod.Wrap(ErrInvalidPacketData, "missed blocks count must be positive")
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
