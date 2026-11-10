package types_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cosmos/gogoproto/proto"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"

	"cosmossdk.io/x/tx/signing"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/address"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/allinbits/vaas/x/vaas/consumer/types"
)

const msgTestOwner = "cosmos1dkas8mu4kyhl5jrh4nzvm65qz588hy9qcz08la"

func TestMsgOwnerExecValidateBasic(t *testing.T) {
	inner := &clienttypes.MsgUpdateParams{Signer: types.OwnerExecAuthority().String(), Params: clienttypes.DefaultParams()}
	good, err := types.NewMsgOwnerExec(msgTestOwner, []sdk.Msg{inner})
	require.NoError(t, err)
	require.NoError(t, good.ValidateBasic())

	badSigner := *good
	badSigner.Signer = "not-an-address"
	require.Error(t, badSigner.ValidateBasic())

	empty := *good
	empty.Messages = nil
	require.Error(t, empty.ValidateBasic())

	hollow := *good
	hollow.Messages = []*codectypes.Any{{}}
	require.Error(t, hollow.ValidateBasic())
}

// TestMsgOwnerExecRoundTripsThroughTheCodec: the carried messages survive
// encoding, as a transaction's would, and come back as the messages packed.
func TestMsgOwnerExecRoundTripsThroughTheCodec(t *testing.T) {
	registry, err := codectypes.NewInterfaceRegistryWithOptions(codectypes.InterfaceRegistryOptions{
		ProtoFiles: proto.HybridResolver,
		SigningOptions: signing.Options{
			AddressCodec:          address.NewBech32Codec("cosmos"),
			ValidatorAddressCodec: address.NewBech32Codec("cosmosvaloper"),
		},
	})
	require.NoError(t, err)
	types.RegisterInterfaces(registry)
	clienttypes.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)

	inner := &clienttypes.MsgUpdateParams{Signer: types.OwnerExecAuthority().String(), Params: clienttypes.DefaultParams()}
	msg, err := types.NewMsgOwnerExec(msgTestOwner, []sdk.Msg{inner})
	require.NoError(t, err)

	bz, err := cdc.Marshal(msg)
	require.NoError(t, err)
	var decoded types.MsgOwnerExec
	require.NoError(t, cdc.Unmarshal(bz, &decoded))
	msgs, err := decoded.GetMsgs()
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Equal(t, inner, msgs[0])

	signers, _, err := cdc.GetMsgV1Signers(msgs[0])
	require.NoError(t, err)
	require.Equal(t, [][]byte{authtypes.NewModuleAddress(types.ModuleName)}, signers)
}
