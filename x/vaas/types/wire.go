package types

import (
	"encoding/json"
	"fmt"

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

// MaxMissed returns the number of missed blocks in a full window that
// qualifies as a downtime infraction under these params:
// W - ceil(minSigned * W). The single source of this consensus-critical
// formula: the consumer's window close and the provider's evidence threshold
// check both go through it.
func (p DowntimeParams) MaxMissed() int64 {
	return p.SignedBlocksWindow - p.MinSignedPerWindow.MulInt64(p.SignedBlocksWindow).Ceil().TruncateInt64()
}

// EvidencePacketData is sent from a consumer chain to the provider chain
// to report a validator downtime infraction detected on the consumer,
// together with the missed-block bitmap over the reporting window.
type EvidencePacketData struct {
	ValidatorAddr      sdk.ConsAddress         `json:"validator_addr"`
	WindowEndHeight    int64                   `json:"window_end_height"` // last height of the window
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
		WindowEndHeight:    windowStart + span - 1,
		Infraction:         stakingtypes.Infraction_INFRACTION_DOWNTIME,
		WindowStartHeight:  windowStart,
		MissedBlocksBitmap: bitmap,
		SignedBlocksWindow: window,
		MinSignedPerWindow: minSigned,
	}
}

// Span returns the number of heights covered by the reporting window.
func (e EvidencePacketData) Span() int64 {
	return e.WindowEndHeight - e.WindowStartHeight + 1
}

// MissedCount returns the number of missed blocks recorded in the bitmap
// over [0, Span()), ignoring any trailing bits beyond the span.
func (e EvidencePacketData) MissedCount() int64 {
	span := e.Span()
	var count int64
	for i := int64(0); i < span; i++ {
		if BitmapIsSet(e.MissedBlocksBitmap, i) {
			count++
		}
	}
	return count
}

// MaxMissed returns the number of missed blocks in a full window that
// qualifies as a downtime infraction, computed from the packet's own echoed
// window parameters (see DowntimeParams.MaxMissed) so the provider polices
// exactly the threshold the consumer claims to have computed against
// (subject to AcceptableDowntimeParams verifying those echoed fields are
// themselves acceptable).
func (e EvidencePacketData) MaxMissed() int64 {
	return DowntimeParams{
		SignedBlocksWindow: e.SignedBlocksWindow,
		MinSignedPerWindow: e.MinSignedPerWindow,
	}.MaxMissed()
}

// Validate returns an error if the EvidencePacketData is invalid.
func (e EvidencePacketData) Validate() error {
	if len(e.ValidatorAddr) == 0 {
		return errorsmod.Wrap(ErrInvalidPacketData, "validator address cannot be empty")
	}
	if e.WindowEndHeight <= 0 {
		return errorsmod.Wrap(ErrInvalidPacketData, "window end height must be positive")
	}
	if e.Infraction != stakingtypes.Infraction_INFRACTION_DOWNTIME {
		return fmt.Errorf("only DOWNTIME infractions can be sent as evidence packets, got %s", e.Infraction)
	}
	if e.WindowStartHeight <= 0 {
		return errorsmod.Wrap(ErrInvalidPacketData, "window start height must be positive")
	}
	if e.WindowEndHeight < e.WindowStartHeight {
		return errorsmod.Wrap(ErrInvalidPacketData, "window end height cannot be before window start height")
	}
	if e.SignedBlocksWindow <= 0 {
		return errorsmod.Wrap(ErrInvalidPacketData, "signed blocks window must be positive")
	}
	if e.Span() > e.SignedBlocksWindow {
		return errorsmod.Wrap(ErrInvalidPacketData, "window span cannot exceed signed blocks window")
	}
	wantBitmapLen := (e.Span() + 7) / 8
	if int64(len(e.MissedBlocksBitmap)) != wantBitmapLen {
		return errorsmod.Wrap(ErrInvalidPacketData, "missed blocks bitmap length does not match window span")
	}
	// A JSON payload omitting min_signed_per_window leaves the dec nil-backed;
	// the range checks below would panic on it.
	if e.MinSignedPerWindow.IsNil() {
		return errorsmod.Wrap(ErrInvalidPacketData, "min signed per window cannot be nil")
	}
	if e.MinSignedPerWindow.LTE(math.LegacyZeroDec()) || e.MinSignedPerWindow.GTE(math.LegacyOneDec()) {
		return errorsmod.Wrap(ErrInvalidPacketData, "min signed per window must be between 0 and 1")
	}
	if e.MissedCount() <= 0 {
		return errorsmod.Wrap(ErrInvalidPacketData, "missed blocks count must be positive")
	}
	return nil
}

// GetBytes marshals the EvidencePacketData into JSON bytes for IBC transport.
func (e EvidencePacketData) GetBytes() []byte {
	bz, err := json.Marshal(&e)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal EvidencePacketData: %v", err))
	}
	return bz
}
