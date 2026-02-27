package types

import (
	errorsmod "cosmossdk.io/errors"
)

// Consumer sentinel errors
var (
	ErrNoProposerChannelId        = errorsmod.Register(ModuleName, 1, "no established VAAS channel")
	ErrConsumerAccountUnderfunded = errorsmod.Register(ModuleName, 2, "consumer fee collector account is underfunded")
)
