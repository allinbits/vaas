package types

import (
	errorsmod "cosmossdk.io/errors"
)

// VAAS sentinel errors
var (
	ErrInvalidPacketData           = errorsmod.Register(ModuleName, 1, "invalid VAAS packet data")
	ErrInvalidVersion              = errorsmod.Register(ModuleName, 2, "invalid VAAS version")
	ErrInvalidChannelFlow          = errorsmod.Register(ModuleName, 3, "invalid message sent to channel end")
	ErrInvalidGenesis              = errorsmod.Register(ModuleName, 4, "invalid genesis state")
	ErrDuplicateChannel            = errorsmod.Register(ModuleName, 5, "VAAS channel already exists")
	ErrInvalidHandshakeMetadata    = errorsmod.Register(ModuleName, 6, "invalid provider handshake metadata")
	ErrChannelNotFound             = errorsmod.Register(ModuleName, 7, "channel not found")
	ErrClientNotFound              = errorsmod.Register(ModuleName, 8, "client not found")
	ErrInvalidConsumerState        = errorsmod.Register(ModuleName, 9, "provider chain has invalid state for consumer chain")
	ErrInvalidConsumerClient       = errorsmod.Register(ModuleName, 10, "VAAS channel is not built on correct client")
	ErrInvalidProposal             = errorsmod.Register(ModuleName, 11, "invalid proposal")
	ErrDuplicateConsumerChain      = errorsmod.Register(ModuleName, 12, "consumer chain already exists")
	ErrConsumerChainNotFound       = errorsmod.Register(ModuleName, 13, "consumer chain not found")
	ErrInvalidDoubleVotingEvidence = errorsmod.Register(ModuleName, 14, "invalid consumer double voting evidence")
	ErrStoreKeyNotFound            = errorsmod.Register(ModuleName, 15, "store key not found")
	ErrStoreUnmarshal              = errorsmod.Register(ModuleName, 16, "cannot unmarshal value from store")
	ErrInvalidConsumerId           = errorsmod.Register(ModuleName, 17, "invalid consumer id")
)
