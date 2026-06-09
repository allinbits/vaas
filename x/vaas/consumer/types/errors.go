package types

import (
	errorsmod "cosmossdk.io/errors"
)

// Consumer sentinel errors
var (
	ErrInvalidProviderClient = errorsmod.Register(ModuleName, 2, "invalid provider client")
	ErrConsumerInDebt        = errorsmod.Register(ModuleName, 3, "consumer chain is in debt")
	ErrInvalidFeeDenom       = errorsmod.Register(ModuleName, 4, "invalid fee denom: consumer requires photon")
)
