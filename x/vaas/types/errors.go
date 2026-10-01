package types

import (
	errorsmod "cosmossdk.io/errors"
)

// VAAS sentinel errors
var (
	ErrInvalidPacketData           = errorsmod.Register(ModuleName, 1, "invalid VAAS packet data")
	ErrInvalidGenesis              = errorsmod.Register(ModuleName, 2, "invalid genesis state")
	ErrClientNotFound              = errorsmod.Register(ModuleName, 3, "client not found")
	ErrInvalidConsumerState        = errorsmod.Register(ModuleName, 4, "provider chain has invalid state for consumer chain")
	ErrInvalidDoubleVotingEvidence = errorsmod.Register(ModuleName, 5, "invalid consumer double voting evidence")
)
