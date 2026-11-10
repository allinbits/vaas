package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"

	errorsmod "cosmossdk.io/errors"

	"github.com/cosmos/cosmos-sdk/baseapp"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
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
}

// newOwnerExecFixture seeds the owner into the consumer params and wires a
// router that executes the IBC client parameter update, the kind of message
// the owner is for.
func newOwnerExecFixture(t *testing.T) *ownerExecFixture {
	t.Helper()
	k, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	t.Cleanup(ctrl.Finish)

	params := vaastypes.DefaultConsumerParams()
	params.OwnerAddress = pinOwner
	k.SetParams(ctx, params)

	router := &recordingRouter{typeURL: sdk.MsgTypeURL(&clienttypes.MsgUpdateParams{})}
	k.SetMessageRouter(router)
	return &ownerExecFixture{k: k, ctx: ctx.WithEventManager(sdk.NewEventManager()), srv: consumerkeeper.NewMsgServerImpl(&k), router: router}
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
