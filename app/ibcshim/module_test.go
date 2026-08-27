package ibcshim

import (
	"testing"

	"github.com/stretchr/testify/require"

	clientv2types "github.com/cosmos/ibc-go/v10/modules/core/02-client/v2/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

type recordingSetter struct {
	set map[string]clientv2types.CounterpartyInfo
}

func (r *recordingSetter) SetClientCounterparty(_ sdk.Context, clientID string, cp clientv2types.CounterpartyInfo) {
	r.set[clientID] = cp
}

func entry(clientID, counterpartyID string) clientv2types.GenesisCounterpartyInfo {
	return clientv2types.GenesisCounterpartyInfo{
		ClientId: clientID,
		CounterpartyInfo: clientv2types.CounterpartyInfo{
			ClientId:     counterpartyID,
			MerklePrefix: [][]byte{[]byte("ibc")},
		},
	}
}

// The reason this package exists: two fresh chains both name their first
// client 07-tendermint-0, ibc-go's own RegisterCounterparty accepts the
// pairing, and only its genesis Validate rejects it, so an exported state
// cannot be re-imported. Upstream's clientv2.InitGenesis panics on this input.
func TestInitClientV2GenesisAcceptsCollidingCrossChainIds(t *testing.T) {
	r := &recordingSetter{set: map[string]clientv2types.CounterpartyInfo{}}
	gs := clientv2types.GenesisState{
		CounterpartyInfos: []clientv2types.GenesisCounterpartyInfo{
			entry("07-tendermint-0", "07-tendermint-0"),
		},
	}

	require.NotPanics(t, func() { initClientV2Genesis(sdk.Context{}, r, gs) })
	require.Equal(t, "07-tendermint-0", r.set["07-tendermint-0"].ClientId,
		"the counterparty must be restored exactly as exported")

	// The guard being skipped is real: upstream still rejects this input.
	require.Error(t, gs.Validate(), "if upstream stops rejecting this, delete the ibcshim package")
}

// Every other upstream check is retained.
func TestInitClientV2GenesisKeepsTheOtherChecks(t *testing.T) {
	cases := []struct {
		name string
		gs   clientv2types.GenesisState
	}{
		{"empty client id", clientv2types.GenesisState{CounterpartyInfos: []clientv2types.GenesisCounterpartyInfo{entry("", "07-tendermint-1")}}},
		{"empty merkle prefix", clientv2types.GenesisState{CounterpartyInfos: []clientv2types.GenesisCounterpartyInfo{{ClientId: "07-tendermint-0", CounterpartyInfo: clientv2types.CounterpartyInfo{ClientId: "07-tendermint-1"}}}}},
		{"duplicate client id", clientv2types.GenesisState{CounterpartyInfos: []clientv2types.GenesisCounterpartyInfo{entry("07-tendermint-0", "07-tendermint-1"), entry("07-tendermint-0", "07-tendermint-2")}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &recordingSetter{set: map[string]clientv2types.CounterpartyInfo{}}
			require.Panics(t, func() { initClientV2Genesis(sdk.Context{}, r, tc.gs) })
		})
	}
}
