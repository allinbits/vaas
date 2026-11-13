package types_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/allinbits/vaas/x/vaas/consumer/types"
)

// TestLegacyAminoNamesEveryMsg pins the amino registration a Ledger or any
// SIGN_MODE_LEGACY_AMINO_JSON signer depends on: every consumer Msg the
// interface registry knows marshals through the module's legacy amino codec
// under its registered name, so none is dropped and none renamed.
func TestLegacyAminoNamesEveryMsg(t *testing.T) {
	cdc := codec.NewLegacyAmino()
	types.RegisterLegacyAminoCodec(cdc)

	cases := []struct {
		msg  sdk.Msg
		name string
	}{
		{&types.MsgUpdateParams{}, "vaas/MsgUpdateConsumerParams"},
		{&types.MsgSetProviderClient{}, "vaas/MsgSetProviderClient"},
		{&types.MsgOwnerExec{}, "vaas/MsgOwnerExec"},
	}

	registry := codectypes.NewInterfaceRegistry()
	sdk.RegisterInterfaces(registry)
	types.RegisterInterfaces(registry)
	covered := make([]string, 0, len(cases))
	for _, tc := range cases {
		covered = append(covered, sdk.MsgTypeURL(tc.msg))
	}
	require.ElementsMatch(t, registry.ListImplementations(sdk.MsgInterfaceProtoName), covered, "every registered Msg has a row")

	for _, tc := range cases {
		bz, err := cdc.MarshalJSON(tc.msg)
		require.NoError(t, err, sdk.MsgTypeURL(tc.msg))
		var wrapped struct {
			Type string `json:"type"`
		}
		require.NoError(t, json.Unmarshal(bz, &wrapped), sdk.MsgTypeURL(tc.msg))
		require.Equal(t, tc.name, wrapped.Type, sdk.MsgTypeURL(tc.msg))
	}
}
