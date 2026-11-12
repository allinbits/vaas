package types

import (
	errorsmod "cosmossdk.io/errors"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	sdktx "github.com/cosmos/cosmos-sdk/types/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
)

var _ codectypes.UnpackInterfacesMessage = &MsgOwnerExec{}

// ValidateBasic enforces that the signer is a bech32 address and that a client
// id is present. Whether the signer is the seeded owner or the chain's
// authority, and everything about the client itself, can only be judged
// against state, so SetProviderClient decides.
func (msg MsgSetProviderClient) ValidateBasic() error {
	if _, _, err := bech32.DecodeAndConvert(msg.Signer); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid signer: %s", err)
	}
	if msg.ClientId == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "client id must not be empty")
	}
	return nil
}

// OwnerExecAuthority is the consumer module account: the signer of every
// message a MsgOwnerExec carries, and the authority the reference consumer
// app names for the keepers its owner may act on.
func OwnerExecAuthority() sdk.AccAddress {
	return authtypes.NewModuleAddress(ModuleName)
}

// NewMsgOwnerExec packs msgs for the owner at signer to execute.
func NewMsgOwnerExec(signer string, msgs []sdk.Msg) (*MsgOwnerExec, error) {
	anys, err := sdktx.SetMsgs(msgs)
	if err != nil {
		return nil, err
	}
	return &MsgOwnerExec{Signer: signer, Messages: anys}, nil
}

// GetMsgs unpacks the carried messages.
func (msg *MsgOwnerExec) GetMsgs() ([]sdk.Msg, error) {
	return sdktx.GetMsgs(msg.Messages, "sdk.MsgOwnerExec")
}

// UnpackInterfaces implements codectypes.UnpackInterfacesMessage.
func (msg MsgOwnerExec) UnpackInterfaces(unpacker codectypes.AnyUnpacker) error {
	return sdktx.UnpackInterfaces(unpacker, msg.Messages)
}

// ValidateBasic enforces that the signer is a bech32 address and that at
// least one message is carried; the signer's ownership, each message's
// signer and the messages themselves are judged against state and the
// router, so OwnerExec decides.
func (msg MsgOwnerExec) ValidateBasic() error {
	if _, _, err := bech32.DecodeAndConvert(msg.Signer); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid signer: %s", err)
	}
	if len(msg.Messages) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "no messages to execute")
	}
	for i, any := range msg.Messages {
		if any == nil || any.TypeUrl == "" {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "message %d is empty", i)
		}
	}
	return nil
}
