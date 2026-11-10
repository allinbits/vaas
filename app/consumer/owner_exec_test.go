package app_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"

	upgradetypes "cosmossdk.io/x/upgrade/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"

	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
)

// TestOwnerExecHoldsTheReferenceConsumersAuthorities proves the wiring the
// message exists for: on the reference consumer the consumer module account
// is the authority of the IBC, upgrade and consumer keepers (among others),
// and the owner reaches them through MsgOwnerExec, signed and admitted like
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
	inner := []sdk.Msg{
		&consumertypes.MsgUpdateParams{Authority: authority, Params: updatedParams},
		&clienttypes.MsgUpdateParams{Signer: authority, Params: clienttypes.NewParams("07-tendermint")},
		&upgradetypes.MsgCancelUpgrade{Authority: authority},
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
