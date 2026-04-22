package types

import (
	errorsmod "cosmossdk.io/errors"
)

// Consumer sentinel errors
var (
	ErrNoProposerChannelId   = errorsmod.Register(ModuleName, 1, "no established VAAS channel")
	ErrInvalidProviderClient = errorsmod.Register(ModuleName, 2, "invalid provider client")
	ErrConsumerInDebt        = errorsmod.Register(ModuleName, 3, "consumer chain is in debt")
)
