package app_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
)

// TestRotateConsPubKeyAsksTheProviderHook proves the provider app wires
// BeforeConsensusPubKeyRotation where x/staking asks it: a
// MsgRotateConsPubKey handled through the app's own message router is refused
// with ErrConsumerKeyInUse when its new key is another validator's assigned
// consumer key, and admitted when the key is free. The keeper tests call the
// hook directly; this is what catches an SDK bump or an app change under
// which the handler stops asking it.
func TestRotateConsPubKeyAsksTheProviderHook(t *testing.T) {
	const chainID = "provider-test-1"
	providerApp, vals := newProviderAppWithBondedValidators(t, chainID, 2, time.Now().UTC())
	rotating, holder := vals[0], vals[1]
	ctx := providerApp.NewContextLegacy(true, cmtproto.Header{Height: providerApp.LastBlockHeight(), ChainID: chainID})

	// The holder has assigned takenKey as its consumer key on a launched consumer.
	consumerID := providerApp.ProviderKeeper.FetchAndIncrementConsumerId(ctx)
	providerApp.ProviderKeeper.SetConsumerPhase(ctx, consumerID, providertypes.CONSUMER_PHASE_LAUNCHED)
	takenKey := newConsensusKey(t)
	providerApp.ProviderKeeper.SetValidatorByConsumerAddr(ctx, consumerID,
		providertypes.NewConsumerConsAddress(sdk.ConsAddress(takenKey.Address())),
		providertypes.NewProviderConsAddress(holder.consAddr))

	rotate := func(newKey cryptotypes.PubKey) error {
		pkAny, err := codectypes.NewAnyWithValue(newKey)
		require.NoError(t, err)
		msg := &stakingtypes.MsgRotateConsPubKey{ValidatorAddress: rotating.valAddr.String(), NewPubkey: pkAny}
		handler := providerApp.MsgServiceRouter().Handler(msg)
		require.NotNil(t, handler, "MsgRotateConsPubKey has no handler")
		_, err = handler(ctx, msg)
		return err
	}

	require.ErrorIs(t, rotate(takenKey), providertypes.ErrConsumerKeyInUse,
		"a rotation onto another validator's consumer key must be refused by the hook")

	// The same path admits a key no consumer holds, so the refusal above is
	// the hook's and not an earlier check's.
	require.NoError(t, rotate(newConsensusKey(t)))
}
