package app_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"

	upgradetypes "cosmossdk.io/x/upgrade/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	consensustypes "github.com/cosmos/cosmos-sdk/x/consensus/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"

	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
)

// TestOwnerExecHoldsTheReferenceConsumersAuthorities proves the wiring the
// message exists for: on the reference consumer the consumer module account
// is the authority of the IBC, upgrade, consensus and consumer keepers (among
// others), and the owner reaches them through MsgOwnerExec, signed and admitted like
// any transaction, with the inner messages executed by the app's own router.
// The governance module account, which no module here can act as, is not an
// accepted signer of the carried messages.
func TestOwnerExecHoldsTheReferenceConsumersAuthorities(t *testing.T) {
	capp := setupAnteTestApp(t, false)
	ctx := anteTestContext(capp)
	priv, owner := fundedSigner(t, capp, ctx, sdk.NewCoins(sdk.NewInt64Coin("stake", 1_000_000)))

	params := capp.ConsumerKeeper.GetConsumerParams(ctx)
	params.OwnerAddress = owner.String()
	capp.ConsumerKeeper.SetParams(ctx, params)
	authority := consumertypes.OwnerExecAuthority().String()

	updatedParams := params
	updatedParams.HistoricalEntries = params.HistoricalEntries + 1
	consensusBefore, err := capp.ConsensusParamsKeeper.Params(ctx, &consensustypes.QueryParamsRequest{})
	require.NoError(t, err)
	block := &cmtproto.BlockParams{MaxBytes: consensusBefore.Params.Block.MaxBytes, MaxGas: consensusBefore.Params.Block.MaxGas / 2}
	evidence := &cmtproto.EvidenceParams{
		MaxAgeNumBlocks: consensusBefore.Params.Evidence.MaxAgeNumBlocks + 1,
		MaxAgeDuration:  consensusBefore.Params.Evidence.MaxAgeDuration,
		MaxBytes:        consensusBefore.Params.Evidence.MaxBytes,
	}
	validator := &cmtproto.ValidatorParams{PubKeyTypes: append(consensusBefore.Params.Validator.PubKeyTypes, cmttypes.ABCIPubKeyTypeSecp256k1)}
	inner := []sdk.Msg{
		&consumertypes.MsgUpdateParams{Authority: authority, Params: updatedParams},
		&clienttypes.MsgUpdateParams{Signer: authority, Params: clienttypes.NewParams("07-tendermint")},
		&upgradetypes.MsgCancelUpgrade{Authority: authority},
		&consensustypes.MsgUpdateParams{Authority: authority, Block: block, Evidence: evidence, Validator: validator},
	}
	msg, err := consumertypes.NewMsgOwnerExec(owner.String(), inner)
	require.NoError(t, err)

	// Admitted: the ante chain judges the owner exec by what it carries, and
	// in normal mode everything passes.
	tx := signedTx(t, capp, ctx, priv, owner, sdk.NewCoins(sdk.NewInt64Coin("stake", 1000)), msg)
	_, err = newAppAnteHandler(t, capp)(ctx, tx, false)
	require.NoError(t, err)

	// Executed: each keeper accepts the consumer module account as its
	// authority.
	handler := capp.MsgServiceRouter().Handler(msg)
	require.NotNil(t, handler)
	_, err = handler(ctx, msg)
	require.NoError(t, err)
	require.Equal(t, updatedParams.HistoricalEntries, capp.ConsumerKeeper.GetConsumerParams(ctx).HistoricalEntries)
	require.Equal(t, []string{"07-tendermint"}, capp.IBCKeeper.ClientKeeper.GetParams(ctx).AllowedClients)
	consensusAfter, err := capp.ConsensusParamsKeeper.Params(ctx, &consensustypes.QueryParamsRequest{})
	require.NoError(t, err)
	require.Equal(t, block, consensusAfter.Params.Block)
	require.Equal(t, evidence, consensusAfter.Params.Evidence)
	require.Equal(t, validator, consensusAfter.Params.Validator)

	// The governance module account is not an authority anyone here can act
	// as, and the owner cannot act as it.
	gov := authtypes.NewModuleAddress(govtypes.ModuleName).String()
	asGov, err := consumertypes.NewMsgOwnerExec(owner.String(), []sdk.Msg{&clienttypes.MsgUpdateParams{Signer: gov, Params: clienttypes.DefaultParams()}})
	require.NoError(t, err)
	_, err = handler(ctx, asGov)
	require.ErrorIs(t, err, consumertypes.ErrInvalidOwnerExec)

	// Nor can anyone but the owner submit it.
	_, stranger := fundedSigner(t, capp, ctx, sdk.NewCoins())
	asStranger, err := consumertypes.NewMsgOwnerExec(stranger.String(), inner)
	require.NoError(t, err)
	_, err = handler(ctx, asStranger)
	require.ErrorIs(t, err, consumertypes.ErrInvalidOwnerExec)
}

// TestOwnerExecKeepsAnOwnerOnTheReferenceConsumer runs the consumer's own
// parameter update through the real router: an update that leaves
// owner_address empty, as one written without it does (the message carries
// the full params), or names the consumer module account, is refused and
// changes nothing; one naming another account hands the consumer over.
func TestOwnerExecKeepsAnOwnerOnTheReferenceConsumer(t *testing.T) {
	capp := setupAnteTestApp(t, false)
	ctx := anteTestContext(capp)
	_, owner := fundedSigner(t, capp, ctx, sdk.NewCoins())
	_, successor := fundedSigner(t, capp, ctx, sdk.NewCoins())

	params := capp.ConsumerKeeper.GetConsumerParams(ctx)
	params.OwnerAddress = owner.String()
	capp.ConsumerKeeper.SetParams(ctx, params)
	authority := consumertypes.OwnerExecAuthority()

	execWithOwner := func(newOwner string) sdk.Msg {
		updated := params
		updated.OwnerAddress = newOwner
		msg, err := consumertypes.NewMsgOwnerExec(owner.String(), []sdk.Msg{
			&consumertypes.MsgUpdateParams{Authority: authority.String(), Params: updated},
		})
		require.NoError(t, err)
		return msg
	}
	handler := capp.MsgServiceRouter().Handler(execWithOwner(""))
	require.NotNil(t, handler)

	for _, newOwner := range []string{"", authority.String()} {
		cacheCtx, _ := ctx.CacheContext()
		_, err := handler(cacheCtx, execWithOwner(newOwner))
		require.ErrorIs(t, err, consumertypes.ErrInvalidOwnerExec, "owner %q", newOwner)
	}
	require.Equal(t, owner.String(), capp.ConsumerKeeper.GetConsumerParams(ctx).OwnerAddress)

	_, err := handler(ctx, execWithOwner(successor.String()))
	require.NoError(t, err)
	require.Equal(t, successor.String(), capp.ConsumerKeeper.GetConsumerParams(ctx).OwnerAddress)
}

// TestConsumerModuleAccountRefusesFunds: the consumer module account, whose
// funds the owner could move through an owner exec, is a blocked address, so
// a send to it is refused rather than becoming the owner's.
func TestConsumerModuleAccountRefusesFunds(t *testing.T) {
	capp := setupAnteTestApp(t, false)
	ctx := anteTestContext(capp)
	authority := consumertypes.OwnerExecAuthority()
	require.True(t, capp.BankKeeper.BlockedAddr(authority))

	_, sender := fundedSigner(t, capp, ctx, sdk.NewCoins(sdk.NewInt64Coin("stake", 1000)))
	send := &banktypes.MsgSend{FromAddress: sender.String(), ToAddress: authority.String(), Amount: sdk.NewCoins(sdk.NewInt64Coin("stake", 500))}
	handler := capp.MsgServiceRouter().Handler(send)
	require.NotNil(t, handler)
	_, err := handler(ctx, send)
	require.ErrorIs(t, err, sdkerrors.ErrUnauthorized)
	require.True(t, capp.BankKeeper.GetAllBalances(ctx, authority).IsZero())
}
