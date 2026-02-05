package types

import (
	errorsmod "cosmossdk.io/errors"
)

// Consumer sentinel errors
var (
	ErrNoProposerChannelId = errorsmod.Register(ModuleName, 1, "no established VAAS channel")
	// ErrInvalidProviderClient is returned when a packet is received from an unexpected client.
	// IBC v2 Note: This error is used when a VSC packet arrives from a client other than
	// the established provider client.
	ErrInvalidProviderClient = errorsmod.Register(ModuleName, 2, "invalid provider client")
)
