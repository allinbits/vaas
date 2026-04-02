package types

import (
	errorsmod "cosmossdk.io/errors"
)

var ErrInvalidProviderClient = errorsmod.Register(ModuleName, 2, "invalid provider client")
