package types

import (
	errorsmod "cosmossdk.io/errors"

	"github.com/cosmos/cosmos-sdk/types/bech32"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

// ValidateBasic enforces that the signer is a bech32 address and that a client
// id is present. Whether the signer is the seeded owner or the governance
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
