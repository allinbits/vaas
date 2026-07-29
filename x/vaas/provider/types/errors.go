package types

import (
	errorsmod "cosmossdk.io/errors"
)

// Provider sentinel errors
var (
	ErrUnknownConsumerId                       = errorsmod.Register(ModuleName, 1, "no consumer chain with this consumer id")
	ErrConsumerKeyInUse                        = errorsmod.Register(ModuleName, 2, "consumer key is already in use by a validator")
	ErrCannotAssignDefaultKeyAssignment        = errorsmod.Register(ModuleName, 3, "cannot re-assign default key assignment")
	ErrInvalidConsumerClient                   = errorsmod.Register(ModuleName, 4, "invalid consumer client")
	ErrNoUnbondingTime                         = errorsmod.Register(ModuleName, 5, "provider unbonding time not found")
	ErrUnauthorized                            = errorsmod.Register(ModuleName, 6, "unauthorized")
	ErrInvalidPhase                            = errorsmod.Register(ModuleName, 7, "cannot perform action in the current phase of consumer chain")
	ErrInvalidConsumerMetadata                 = errorsmod.Register(ModuleName, 8, "invalid consumer metadata")
	ErrInvalidConsumerInitializationParameters = errorsmod.Register(ModuleName, 9, "invalid consumer initialization parameters")
	ErrNoOwnerAddress                          = errorsmod.Register(ModuleName, 10, "missing owner address")
	ErrInvalidNewOwnerAddress                  = errorsmod.Register(ModuleName, 11, "invalid new owner address")
	ErrInvalidMsgCreateConsumer                = errorsmod.Register(ModuleName, 12, "invalid create consumer message")
	ErrInvalidMsgUpdateConsumer                = errorsmod.Register(ModuleName, 13, "invalid update consumer message")
	ErrInvalidMsgAssignConsumerKey             = errorsmod.Register(ModuleName, 14, "invalid assign consumer key message")
	ErrInvalidMsgSubmitConsumerMisbehaviour    = errorsmod.Register(ModuleName, 15, "invalid submit consumer misbehaviour message")
	ErrInvalidMsgSubmitConsumerDoubleVoting    = errorsmod.Register(ModuleName, 16, "invalid submit consumer double voting message")
	ErrDuplicateChainId                        = errorsmod.Register(ModuleName, 17, "consumer chain-id is already in use")
	ErrPoolEmpty                               = errorsmod.Register(ModuleName, 18, "consumer fee pool has zero balance for the requested denom")
	ErrUnsolicitedFeePoolDeposit               = errorsmod.Register(ModuleName, 19, "direct sends to consumer fee pool addresses are not permitted; use MsgFundConsumerFeePool")
	ErrInvalidFundDenom                        = errorsmod.Register(ModuleName, 20, "deposit denom does not match the current fees_per_block denom")
	ErrDepositTooSmall                         = errorsmod.Register(ModuleName, 21, "deposit too small to mint any shares")
	ErrSubShareWithdraw                        = errorsmod.Register(ModuleName, 22, "withdraw amount too small to burn any shares")
	ErrNoSharesForDepositor                    = errorsmod.Register(ModuleName, 23, "depositor has no shares in the consumer fee pool for the requested denom")
	ErrDepositBelowMinimum                     = errorsmod.Register(ModuleName, 24, "deposit is below the min-deposit floor")
	ErrFeePoolLocked                           = errorsmod.Register(ModuleName, 25, "consumer fee pool is locked while consumer is launched")
	ErrDowntimeChallengeFailed                 = errorsmod.Register(ModuleName, 26, "downtime challenge verification failed")
	ErrConsumerClientNotActive                 = errorsmod.Register(ModuleName, 27, "consumer client is not active")
)
