package types

import (
	"github.com/cosmos/ibc-go/v10/modules/core/exported"
	tendermint "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/legacy"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/msgservice"
)

// RegisterLegacyAminoCodec registers the provider Msg types on the amino codec
// so they can be signed with SIGN_MODE_LEGACY_AMINO_JSON (Ledger and amino-json
// clients). Names are kept under the 40-character amino limit for Ledger.
func RegisterLegacyAminoCodec(cdc *codec.LegacyAmino) {
	legacy.RegisterAminoMsg(cdc, &MsgAssignConsumerKey{}, "vaas/MsgAssignConsumerKey")
	legacy.RegisterAminoMsg(cdc, &MsgCreateConsumer{}, "vaas/MsgCreateConsumer")
	legacy.RegisterAminoMsg(cdc, &MsgUpdateConsumer{}, "vaas/MsgUpdateConsumer")
	legacy.RegisterAminoMsg(cdc, &MsgRemoveConsumer{}, "vaas/MsgRemoveConsumer")
	legacy.RegisterAminoMsg(cdc, &MsgUpdateParams{}, "vaas/MsgUpdateProviderParams")
	legacy.RegisterAminoMsg(cdc, &MsgSubmitConsumerMisbehaviour{}, "vaas/MsgSubmitConsumerMisbehaviour")
	legacy.RegisterAminoMsg(cdc, &MsgSubmitConsumerDoubleVoting{}, "vaas/MsgSubmitConsumerDoubleVoting")
	legacy.RegisterAminoMsg(cdc, &MsgSetConsumerFeesPerBlock{}, "vaas/MsgSetConsumerFeesPerBlock")
	legacy.RegisterAminoMsg(cdc, &MsgFundConsumerFeePool{}, "vaas/MsgFundConsumerFeePool")
	legacy.RegisterAminoMsg(cdc, &MsgWithdrawConsumerFeePool{}, "vaas/MsgWithdrawConsumerFeePool")
	legacy.RegisterAminoMsg(cdc, &MsgSweepConsumerFeePool{}, "vaas/MsgSweepConsumerFeePool")
	legacy.RegisterAminoMsg(cdc, &MsgChallengeConsumerDowntime{}, "vaas/MsgChallengeConsumerDowntime")
	legacy.RegisterAminoMsg(cdc, &MsgResumeConsumer{}, "vaas/MsgResumeConsumer")
}

// RegisterInterfaces registers the provider message types to the interface registry
func RegisterInterfaces(registry codectypes.InterfaceRegistry) {
	registry.RegisterImplementations(
		(*sdk.Msg)(nil),
		&MsgAssignConsumerKey{},
		&MsgCreateConsumer{},
		&MsgUpdateConsumer{},
		&MsgRemoveConsumer{},
		&MsgUpdateParams{},
		&MsgSubmitConsumerMisbehaviour{},
		&MsgSubmitConsumerDoubleVoting{},
		&MsgSetConsumerFeesPerBlock{},
		&MsgFundConsumerFeePool{},
		&MsgWithdrawConsumerFeePool{},
		&MsgSweepConsumerFeePool{},
		&MsgChallengeConsumerDowntime{},
		&MsgResumeConsumer{},
	)
	registry.RegisterImplementations(
		(*exported.ClientMessage)(nil),
		&tendermint.Misbehaviour{},
	)
	msgservice.RegisterMsgServiceDesc(registry, &_Msg_serviceDesc)
}

var (
	amino = codec.NewLegacyAmino()

	// ModuleCdc references the global x/ibc-transfer module codec. Note, the codec
	// should ONLY be used in certain instances of tests and for JSON encoding.
	//
	// The actual codec used for serialization should be provided to x/ibc transfer and
	// defined at the application level.
	ModuleCdc = codec.NewProtoCodec(codectypes.NewInterfaceRegistry())
)

func init() {
	RegisterLegacyAminoCodec(amino)
	amino.Seal()
}
