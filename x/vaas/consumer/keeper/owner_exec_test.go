package keeper_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	abci "github.com/cometbft/cometbft/abci/types"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	errorsmod "cosmossdk.io/errors"

	"github.com/cosmos/cosmos-sdk/baseapp"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	consumerkeeper "github.com/allinbits/vaas/x/vaas/consumer/keeper"
	"github.com/allinbits/vaas/x/vaas/consumer/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// The owner's authority under test: MsgOwnerExec executes the messages the
// consumer's seeded owner submits as the consumer module account, through the
// app's message router, and refuses everything that is not exactly that.

// recordingRouter stands in for the app's message router: it routes one
// message type to a handler that records the message and returns an event,
// and knows no other route.
type recordingRouter struct {
	typeURL  string
	executed []sdk.Msg
	fail     error
}

func (r *recordingRouter) Handler(msg sdk.Msg) baseapp.MsgServiceHandler {
	return r.HandlerByTypeURL(sdk.MsgTypeURL(msg))
}

func (r *recordingRouter) HandlerByTypeURL(typeURL string) baseapp.MsgServiceHandler {
	if typeURL != r.typeURL {
		return nil
	}
	return func(_ sdk.Context, msg sdk.Msg) (*sdk.Result, error) {
		if r.fail != nil {
			return nil, r.fail
		}
		r.executed = append(r.executed, msg)
		return &sdk.Result{Events: []abci.Event{{Type: "executed", Attributes: []abci.EventAttribute{{Key: "type", Value: typeURL}}}}}, nil
	}
}

// ownerExecStranger is an account that is neither the owner nor an authority.
const ownerExecStranger = "cosmos1qypqxpq9qcrsszgse4wwrq4vt3s2r0y8ryqhx7"

type ownerExecFixture struct {
	k      consumerkeeper.Keeper
	ctx    sdk.Context
	srv    types.MsgServer
	router *recordingRouter
	mocks  testkeeper.MockedKeepers
}

// newOwnerExecFixture seeds the owner into the consumer params and wires a
// router that executes the IBC client parameter update, the kind of message
// the owner is for.
func newOwnerExecFixture(t *testing.T) *ownerExecFixture {
	t.Helper()
	k, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	t.Cleanup(ctrl.Finish)

	params := vaastypes.DefaultConsumerParams()
	params.OwnerAddress = pinOwner
	k.SetParams(ctx, params)

	router := &recordingRouter{typeURL: sdk.MsgTypeURL(&clienttypes.MsgUpdateParams{})}
	k.SetMessageRouter(router)
	return &ownerExecFixture{k: k, ctx: ctx.WithEventManager(sdk.NewEventManager()), srv: consumerkeeper.NewMsgServerImpl(&k), router: router, mocks: mocks}
}

func clientParamsUpdate(signer sdk.AccAddress) sdk.Msg {
	return &clienttypes.MsgUpdateParams{Signer: signer.String(), Params: clienttypes.DefaultParams()}
}

func ownerExecOf(t *testing.T, signer string, msgs ...sdk.Msg) *types.MsgOwnerExec {
	t.Helper()
	msg, err := types.NewMsgOwnerExec(signer, msgs)
	require.NoError(t, err)
	return msg
}

func TestOwnerExecExecutesTheOwnersMessagesAsTheConsumerModule(t *testing.T) {
	f := newOwnerExecFixture(t)
	inner := clientParamsUpdate(types.OwnerExecAuthority())

	_, err := f.srv.OwnerExec(f.ctx, ownerExecOf(t, pinOwner, inner, inner))
	require.NoError(t, err)
	require.Len(t, f.router.executed, 2, "every message executes, in order")

	events := f.ctx.EventManager().Events()
	var executed, indices []string
	ownerExecEvents := 0
	for _, e := range events {
		switch e.Type {
		case "executed":
			executed = append(executed, e.Type)
			for _, a := range e.Attributes {
				if a.Key == vaastypes.AttributeOwnerExecMsgIndex {
					indices = append(indices, a.Value)
				}
			}
		case vaastypes.EventTypeOwnerExec:
			ownerExecEvents++
		}
	}
	require.Len(t, executed, 2, "the executed messages' events are emitted")
	require.Equal(t, []string{"0", "1"}, indices, "each tagged with its message index")
	require.Equal(t, 1, ownerExecEvents)
}

// The owner is matched on raw address bytes: the provider seeds it under its
// own bech32 prefix, and the same key signs under the consumer's.
func TestOwnerExecAcceptsTheOwnerUnderAnotherPrefix(t *testing.T) {
	f := newOwnerExecFixture(t)
	ownerBytes, err := sdk.GetFromBech32(pinOwner, "cosmos")
	require.NoError(t, err)
	shardOwner, err := sdk.Bech32ifyAddressBytes("shard", ownerBytes)
	require.NoError(t, err)

	_, err = f.srv.OwnerExec(f.ctx, ownerExecOf(t, shardOwner, clientParamsUpdate(types.OwnerExecAuthority())))
	require.NoError(t, err)
	require.Len(t, f.router.executed, 1)
}

func TestOwnerExecRefusesEveryoneButTheOwner(t *testing.T) {
	f := newOwnerExecFixture(t)
	inner := clientParamsUpdate(types.OwnerExecAuthority())

	_, err := f.srv.OwnerExec(f.ctx, ownerExecOf(t, ownerExecStranger, inner))
	require.ErrorIs(t, err, types.ErrInvalidOwnerExec)
	require.ErrorContains(t, err, "not the consumer owner")

	// The authority the keeper was built with is a module account; it does
	// not submit transactions, and it is not the owner either.
	_, err = f.srv.OwnerExec(f.ctx, ownerExecOf(t, f.k.GetAuthority(), inner))
	require.ErrorIs(t, err, types.ErrInvalidOwnerExec)

	// No seeded owner: no one may.
	params := f.k.GetConsumerParams(f.ctx)
	params.OwnerAddress = ""
	f.k.SetParams(f.ctx, params)
	_, err = f.srv.OwnerExec(f.ctx, ownerExecOf(t, pinOwner, inner))
	require.ErrorIs(t, err, types.ErrInvalidOwnerExec)
	require.Empty(t, f.router.executed)
}

// Every carried message must be signed by the consumer module account alone:
// the owner cannot act as the governance module account, or as anyone else.
func TestOwnerExecRefusesMessagesNotSignedByTheConsumerModule(t *testing.T) {
	f := newOwnerExecFixture(t)

	_, err := f.srv.OwnerExec(f.ctx, ownerExecOf(t, pinOwner, clientParamsUpdate(authtypes.NewModuleAddress(govtypes.ModuleName))))
	require.ErrorIs(t, err, types.ErrInvalidOwnerExec)
	require.ErrorContains(t, err, "must be signed by the consumer module account")

	ownerBytes, err := sdk.GetFromBech32(pinOwner, "cosmos")
	require.NoError(t, err)
	_, err = f.srv.OwnerExec(f.ctx, ownerExecOf(t, pinOwner, clientParamsUpdate(sdk.AccAddress(ownerBytes))))
	require.ErrorIs(t, err, types.ErrInvalidOwnerExec)
	require.Empty(t, f.router.executed)
}

func TestOwnerExecRefusesNestingUnknownRoutesAndFailures(t *testing.T) {
	f := newOwnerExecFixture(t)
	inner := clientParamsUpdate(types.OwnerExecAuthority())

	nested := ownerExecOf(t, pinOwner, inner)
	_, err := f.srv.OwnerExec(f.ctx, ownerExecOf(t, pinOwner, nested))
	require.ErrorIs(t, err, types.ErrInvalidOwnerExec)
	require.ErrorContains(t, err, "nests another owner exec")

	_, err = f.srv.OwnerExec(f.ctx, ownerExecOf(t, pinOwner, &types.MsgUpdateParams{Authority: types.OwnerExecAuthority().String(), Params: vaastypes.DefaultConsumerParams()}))
	require.ErrorIs(t, err, sdkerrors.ErrUnknownRequest, "a message the router does not know is refused")

	f.router.fail = errorsmod.Wrap(sdkerrors.ErrUnauthorized, "the keeper's authority is someone else")
	_, err = f.srv.OwnerExec(f.ctx, ownerExecOf(t, pinOwner, inner))
	require.ErrorIs(t, err, sdkerrors.ErrUnauthorized, "a failing message fails the exec")
	require.Empty(t, f.router.executed)
}

// The consumer module's application port carries the downtime evidence the
// module sends from EndBlock, and OnSendPacket admits the module account as
// the signer, so an exec carrying ibc-go's raw v2 MsgSendPacket on that port
// would let the owner author evidence against any provider validator. It is
// refused before anything executes; a send on another port passes the exec's
// own checks, whatever the router then makes of it.
func TestOwnerExecRefusesSendingOnTheConsumerPort(t *testing.T) {
	f := newOwnerExecFixture(t)
	f.router.typeURL = sdk.MsgTypeURL(&channeltypesv2.MsgSendPacket{})
	sendFrom := func(sourcePort string) sdk.Msg {
		return &channeltypesv2.MsgSendPacket{
			SourceClient:     pinClientID,
			TimeoutTimestamp: 1,
			Payloads: []channeltypesv2.Payload{{
				SourcePort:      sourcePort,
				DestinationPort: vaastypes.ProviderAppID,
				Version:         "1",
				Encoding:        "application/json",
				Value:           []byte(`{"validator_addr":"AQID"}`),
			}},
			Signer: types.OwnerExecAuthority().String(),
		}
	}

	_, err := f.srv.OwnerExec(f.ctx, ownerExecOf(t, pinOwner, sendFrom(vaastypes.ConsumerAppID)))
	require.ErrorIs(t, err, types.ErrInvalidOwnerExec)
	require.ErrorContains(t, err, "sends a packet from the "+vaastypes.ConsumerAppID+" port")
	require.Empty(t, f.router.executed)

	_, err = f.srv.OwnerExec(f.ctx, ownerExecOf(t, pinOwner, sendFrom("transfer")))
	require.NoError(t, err)
	require.Len(t, f.router.executed, 1)
}

// The pin's coherence rule holds for its recovery: an exec that leaves the
// pinned provider client tracking another chain id is refused as a whole.
func TestOwnerExecRefusesLeavingTheProviderClientOnAnotherChain(t *testing.T) {
	f := newOwnerExecFixture(t)
	f.k.SetProviderClientID(f.ctx, pinClientID)
	f.k.SetProviderChainId(f.ctx, pinProviderCID)
	inner := clientParamsUpdate(types.OwnerExecAuthority())

	f.mocks.MockClientKeeper.EXPECT().GetClientState(gomock.Any(), pinClientID).
		Return(&ibctmtypes.ClientState{ChainId: pinProviderCID}, true).Times(1)
	_, err := f.srv.OwnerExec(f.ctx, ownerExecOf(t, pinOwner, inner))
	require.NoError(t, err, "a pin still tracking the provider passes")

	f.mocks.MockClientKeeper.EXPECT().GetClientState(gomock.Any(), pinClientID).
		Return(&ibctmtypes.ClientState{ChainId: "another-chain-1"}, true).Times(1)
	_, err = f.srv.OwnerExec(f.ctx, ownerExecOf(t, pinOwner, inner))
	require.ErrorIs(t, err, types.ErrInvalidOwnerExec)
	require.ErrorContains(t, err, "not the provider chain id")
}

// paramsRouter stands in for the app's router with the consumer's own
// parameter update, storing the params the message carries as the handler
// does (the in-memory keeper's authority is not the consumer module account,
// so the real handler would refuse it).
type paramsRouter struct{ k consumerkeeper.Keeper }

func (r paramsRouter) Handler(msg sdk.Msg) baseapp.MsgServiceHandler {
	return r.HandlerByTypeURL(sdk.MsgTypeURL(msg))
}

func (r paramsRouter) HandlerByTypeURL(typeURL string) baseapp.MsgServiceHandler {
	if typeURL != sdk.MsgTypeURL(&types.MsgUpdateParams{}) {
		return nil
	}
	return func(ctx sdk.Context, msg sdk.Msg) (*sdk.Result, error) {
		r.k.SetParams(ctx, msg.(*types.MsgUpdateParams).Params)
		return &sdk.Result{}, nil
	}
}

// An exec may hand the consumer to another owner, but not leave it with an
// owner nobody can sign as: none at all (a MsgUpdateParams that omits
// owner_address clears it), an undecodable one, or the consumer module
// account itself.
func TestOwnerExecRefusesLeavingNoUsableOwner(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	t.Cleanup(ctrl.Finish)
	params := vaastypes.DefaultConsumerParams()
	params.OwnerAddress = pinOwner
	k.SetParams(ctx, params)
	k.SetMessageRouter(paramsRouter{k: k})
	srv := consumerkeeper.NewMsgServerImpl(&k)
	authority := types.OwnerExecAuthority()

	withOwner := func(owner string) sdk.Msg {
		p := params
		p.OwnerAddress = owner
		return &types.MsgUpdateParams{Authority: authority.String(), Params: p}
	}
	for name, owner := range map[string]string{
		"no owner":                  "",
		"an undecodable owner":      "not-an-address",
		"the module account itself": authority.String(),
	} {
		cacheCtx, _ := ctx.CacheContext()
		_, err := srv.OwnerExec(cacheCtx, ownerExecOf(t, pinOwner, withOwner(owner)))
		require.ErrorIs(t, err, types.ErrInvalidOwnerExec, name)
	}

	successor := sdk.AccAddress(bytes.Repeat([]byte{5}, 20)).String()
	_, err := srv.OwnerExec(ctx, ownerExecOf(t, pinOwner, withOwner(successor)))
	require.NoError(t, err, "a handover to another account passes")
	require.Equal(t, successor, k.GetConsumerParams(ctx).OwnerAddress)
}

// A carried message must have exactly one signer: two, even with the consumer
// module account first, are refused. A bank multi-send names one signer per
// input.
func TestOwnerExecRefusesAMessageWithTwoSigners(t *testing.T) {
	f := newOwnerExecFixture(t)
	ownerBytes, err := sdk.GetFromBech32(pinOwner, "cosmos")
	require.NoError(t, err)
	multiSend := &banktypes.MsgMultiSend{
		Inputs: []banktypes.Input{
			{Address: types.OwnerExecAuthority().String(), Coins: sdk.NewCoins(sdk.NewInt64Coin("stake", 1))},
			{Address: sdk.AccAddress(ownerBytes).String(), Coins: sdk.NewCoins(sdk.NewInt64Coin("stake", 1))},
		},
	}
	_, err = f.srv.OwnerExec(f.ctx, ownerExecOf(t, pinOwner, multiSend))
	require.ErrorIs(t, err, types.ErrInvalidOwnerExec)
	require.ErrorContains(t, err, "must be signed by the consumer module account")
}

func TestOwnerExecRefusesWithoutARouter(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	params := vaastypes.DefaultConsumerParams()
	params.OwnerAddress = pinOwner
	k.SetParams(ctx, params)

	_, err := consumerkeeper.NewMsgServerImpl(&k).OwnerExec(ctx, ownerExecOf(t, pinOwner, clientParamsUpdate(types.OwnerExecAuthority())))
	require.ErrorIs(t, err, types.ErrInvalidOwnerExec)
	require.ErrorContains(t, err, "no message router")
	require.Panics(t, func() { k.SetMessageRouter(&recordingRouter{}); k.SetMessageRouter(&recordingRouter{}) }, "the router is wired once")
}

func TestQueryOwnerAuthority(t *testing.T) {
	f := newOwnerExecFixture(t)
	res, err := f.k.QueryOwnerAuthority(f.ctx, &types.QueryOwnerAuthorityRequest{})
	require.NoError(t, err)
	require.Equal(t, types.OwnerExecAuthority().String(), res.Authority)
	require.Equal(t, pinOwner, res.Owner)
}
