package types_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/allinbits/vaas/x/vaas/provider/types"
)

// TestLegacyAminoMsgNames pins the amino name every provider Msg is registered
// under: a SIGN_MODE_LEGACY_AMINO_JSON signer (a Ledger, say) fails at signing
// on a Msg whose registration is missing or renamed. The table must also cover
// every Msg that RegisterInterfaces knows, so a Msg added to the module without
// an amino registration fails here.
func TestLegacyAminoMsgNames(t *testing.T) {
	cases := []struct {
		msg  sdk.Msg
		name string
	}{
		{&types.MsgAssignConsumerKey{}, "vaas/MsgAssignConsumerKey"},
		{&types.MsgCreateConsumer{}, "vaas/MsgCreateConsumer"},
		{&types.MsgUpdateConsumer{}, "vaas/MsgUpdateConsumer"},
		{&types.MsgRemoveConsumer{}, "vaas/MsgRemoveConsumer"},
		{&types.MsgUpdateParams{}, "vaas/MsgUpdateProviderParams"},
		{&types.MsgSubmitConsumerMisbehaviour{}, "vaas/MsgSubmitConsumerMisbehaviour"},
		{&types.MsgSubmitConsumerDoubleVoting{}, "vaas/MsgSubmitConsumerDoubleVoting"},
		{&types.MsgSetConsumerFeesPerBlock{}, "vaas/MsgSetConsumerFeesPerBlock"},
		{&types.MsgFundConsumerFeePool{}, "vaas/MsgFundConsumerFeePool"},
		{&types.MsgWithdrawConsumerFeePool{}, "vaas/MsgWithdrawConsumerFeePool"},
		{&types.MsgSweepConsumerFeePool{}, "vaas/MsgSweepConsumerFeePool"},
		{&types.MsgChallengeConsumerDowntime{}, "vaas/MsgChallengeConsumerDowntime"},
		{&types.MsgResumeConsumer{}, "vaas/MsgResumeConsumer"},
		{&types.MsgSetConsumerRefusal{}, "vaas/MsgSetConsumerRefusal"},
	}

	cdc := codec.NewLegacyAmino()
	types.RegisterLegacyAminoCodec(cdc)

	covered := make([]string, 0, len(cases))
	for _, tc := range cases {
		bz, err := cdc.MarshalJSON(tc.msg)
		require.NoError(t, err, tc.name)
		var wrapped struct {
			Type string `json:"type"`
		}
		require.NoError(t, json.Unmarshal(bz, &wrapped), tc.name)
		require.Equal(t, tc.name, wrapped.Type)
		covered = append(covered, sdk.MsgTypeURL(tc.msg))
	}

	// The Msg interface itself is registered by cosmos-sdk at the app level.
	registry := codectypes.NewInterfaceRegistry()
	sdk.RegisterInterfaces(registry)
	types.RegisterInterfaces(registry)
	require.ElementsMatch(t, registry.ListImplementations(sdk.MsgInterfaceProtoName), covered,
		"every Msg the interface registry knows must carry an amino registration")
}
