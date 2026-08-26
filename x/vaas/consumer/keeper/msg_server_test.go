package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	clientv2types "github.com/cosmos/ibc-go/v10/modules/core/02-client/v2/types"
	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	consumerkeeper "github.com/allinbits/vaas/x/vaas/consumer/keeper"
	"github.com/allinbits/vaas/x/vaas/consumer/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// The bootstrap pin under test: with no client created at genesis the consumer
// starts with no provider client at all, every VSC packet is rejected, and the
// owner seeded into the consumer params (or governance) pins the
// relayer-created client exactly once with MsgSetProviderClient.

const (
	pinOwner       = "cosmos1dkas8mu4kyhl5jrh4nzvm65qz588hy9qcz08la"
	pinClientID    = "07-tendermint-0"
	pinProviderCID = "provider-1"
)

type pinFixture struct {
	k     consumerkeeper.Keeper
	ctx   sdk.Context
	mocks testkeeper.MockedKeepers
	srv   types.MsgServer
}

func newPinFixture(t *testing.T) *pinFixture {
	t.Helper()
	k, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	t.Cleanup(ctrl.Finish)

	params := vaastypes.DefaultConsumerParams()
	params.Enabled = true
	params.OwnerAddress = pinOwner
	k.SetParams(ctx, params)
	// The provider chain id is pinned at genesis from the provider-authored
	// client state; the declared client must track it.
	k.SetProviderChainId(ctx, pinProviderCID)

	return &pinFixture{k: k, ctx: ctx, mocks: mocks, srv: consumerkeeper.NewMsgServerImpl(&k)}
}

func (f *pinFixture) stubClient(clientID, chainID string, status ibcexported.Status) {
	f.mocks.MockClientKeeper.EXPECT().
		GetClientState(gomock.Any(), clientID).
		Return(&ibctmtypes.ClientState{ChainId: chainID}, true).AnyTimes()
	f.mocks.MockClientKeeper.EXPECT().
		GetClientStatus(gomock.Any(), clientID).
		Return(status).AnyTimes()
}

func TestSetProviderClient(t *testing.T) {
	f := newPinFixture(t)
	f.stubClient(pinClientID, pinProviderCID, ibcexported.Active)
	f.mocks.ClientCounterparties[pinClientID] = clientv2types.CounterpartyInfo{ClientId: "07-tendermint-9"}

	_, err := f.srv.SetProviderClient(f.ctx, &types.MsgSetProviderClient{
		Signer:   pinOwner,
		ClientId: pinClientID,
	})
	require.NoError(t, err)

	pinned, found := f.k.GetProviderClientID(f.ctx)
	require.True(t, found)
	require.Equal(t, pinClientID, pinned)
}

func TestSetProviderClientGovAuthority(t *testing.T) {
	f := newPinFixture(t)
	f.stubClient(pinClientID, pinProviderCID, ibcexported.Active)
	f.mocks.ClientCounterparties[pinClientID] = clientv2types.CounterpartyInfo{ClientId: "07-tendermint-9"}

	_, err := f.srv.SetProviderClient(f.ctx, &types.MsgSetProviderClient{
		Signer:   authtypes.NewModuleAddress(govtypes.ModuleName).String(),
		ClientId: pinClientID,
	})
	require.NoError(t, err, "the governance authority must be able to pin")
}

func TestSetProviderClientRejectsNonOwner(t *testing.T) {
	f := newPinFixture(t)
	f.stubClient(pinClientID, pinProviderCID, ibcexported.Active)
	f.mocks.ClientCounterparties[pinClientID] = clientv2types.CounterpartyInfo{ClientId: "07-tendermint-9"}

	_, err := f.srv.SetProviderClient(f.ctx, &types.MsgSetProviderClient{
		Signer:   "cosmos1qypqxpq9qcrsszgse4wwrq4vt3s2r0y8ryqhx7",
		ClientId: pinClientID,
	})
	require.ErrorIs(t, err, types.ErrInvalidProviderClient)
	_, found := f.k.GetProviderClientID(f.ctx)
	require.False(t, found)
}

func TestSetProviderClientOnlyOnce(t *testing.T) {
	f := newPinFixture(t)
	f.stubClient(pinClientID, pinProviderCID, ibcexported.Active)
	f.mocks.ClientCounterparties[pinClientID] = clientv2types.CounterpartyInfo{ClientId: "07-tendermint-9"}
	_, err := f.srv.SetProviderClient(f.ctx, &types.MsgSetProviderClient{Signer: pinOwner, ClientId: pinClientID})
	require.NoError(t, err)

	other := "07-tendermint-5"
	f.stubClient(other, pinProviderCID, ibcexported.Active)
	f.mocks.ClientCounterparties[other] = clientv2types.CounterpartyInfo{ClientId: "07-tendermint-8"}
	_, err = f.srv.SetProviderClient(f.ctx, &types.MsgSetProviderClient{Signer: pinOwner, ClientId: other})
	require.ErrorIs(t, err, types.ErrInvalidProviderClient,
		"the pin is permanent; a dead client is recovered in place via MsgRecoverClient")

	pinned, _ := f.k.GetProviderClientID(f.ctx)
	require.Equal(t, pinClientID, pinned)
}

func TestSetProviderClientRejectsWrongChainId(t *testing.T) {
	f := newPinFixture(t)
	f.stubClient(pinClientID, "attacker-chain", ibcexported.Active)
	f.mocks.ClientCounterparties[pinClientID] = clientv2types.CounterpartyInfo{ClientId: "07-tendermint-9"}

	_, err := f.srv.SetProviderClient(f.ctx, &types.MsgSetProviderClient{Signer: pinOwner, ClientId: pinClientID})
	require.ErrorIs(t, err, types.ErrInvalidProviderClient)
}

func TestSetProviderClientRejectsNonActive(t *testing.T) {
	f := newPinFixture(t)
	f.stubClient(pinClientID, pinProviderCID, ibcexported.Frozen)
	f.mocks.ClientCounterparties[pinClientID] = clientv2types.CounterpartyInfo{ClientId: "07-tendermint-9"}

	_, err := f.srv.SetProviderClient(f.ctx, &types.MsgSetProviderClient{Signer: pinOwner, ClientId: pinClientID})
	require.ErrorIs(t, err, types.ErrInvalidProviderClient)
}

func TestSetProviderClientRejectsUnroutable(t *testing.T) {
	f := newPinFixture(t)
	f.stubClient(pinClientID, pinProviderCID, ibcexported.Active)
	// no counterparty: nothing can ever be delivered over it

	_, err := f.srv.SetProviderClient(f.ctx, &types.MsgSetProviderClient{Signer: pinOwner, ClientId: pinClientID})
	require.ErrorIs(t, err, types.ErrInvalidProviderClient)
}

// TestSetProviderClientOwnerBech32PrefixInsensitive pins the cross-chain
// wrinkle in the owner seed: the provider writes the owner address it knows,
// rendered under the provider's bech32 prefix, while the message signer is
// rendered under the consumer's. The same key must satisfy the check whatever
// prefix either side used, so authorization compares decoded address bytes.
func TestSetProviderClientOwnerBech32PrefixInsensitive(t *testing.T) {
	f := newPinFixture(t)

	// Same 20 bytes as pinOwner, re-rendered under a different prefix.
	raw, err := sdk.GetFromBech32(pinOwner, "cosmos")
	require.NoError(t, err)
	foreign, err := sdk.Bech32ifyAddressBytes("provider", raw)
	require.NoError(t, err)
	params := f.k.GetConsumerParams(f.ctx)
	params.OwnerAddress = foreign
	f.k.SetParams(f.ctx, params)

	f.stubClient(pinClientID, pinProviderCID, ibcexported.Active)
	f.mocks.ClientCounterparties[pinClientID] = clientv2types.CounterpartyInfo{ClientId: "07-tendermint-9"}

	_, err = f.srv.SetProviderClient(f.ctx, &types.MsgSetProviderClient{
		Signer:   pinOwner, // consumer-prefix rendering of the same key
		ClientId: pinClientID,
	})
	require.NoError(t, err, "owner authorization must compare address bytes, not bech32 strings")
}
