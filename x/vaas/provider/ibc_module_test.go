package provider_test

import (
	"testing"
	"time"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	"github.com/allinbits/vaas/x/vaas/provider"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"
)

func TestIBCModuleOnSendPacketRequiresAuthority(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	module := provider.NewIBCModule(&providerKeeper)
	payload := channeltypesv2.Payload{
		SourcePort:      vaastypes.ProviderAppID,
		DestinationPort: vaastypes.ConsumerAppID,
	}

	authority, err := sdk.AccAddressFromBech32(providerKeeper.GetAuthority())
	require.NoError(t, err)
	require.NoError(t, module.OnSendPacket(ctx, "07-tendermint-0", "07-tendermint-1", 7, payload, authority))

	otherSigner := sdk.AccAddress(ed25519.GenPrivKey().PubKey().Address())
	err = module.OnSendPacket(ctx, "07-tendermint-0", "07-tendermint-1", 7, payload, otherSigner)
	require.ErrorContains(t, err, "different from authority")
}

func TestIBCModuleOnAcknowledgementPacketHandlesErrorSentinel(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerID := "0"
	clientID := "07-tendermint-0"

	providerKeeper.SetConsumerClientId(ctx, consumerID, clientID)
	providerKeeper.SetConsumerPhase(ctx, consumerID, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerID, "consumer-chain")

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(ctx).Return(21*24*time.Hour, nil).Times(1)

	module := provider.NewIBCModule(&providerKeeper)
	err := module.OnAcknowledgementPacket(
		ctx,
		clientID,
		"07-tendermint-1",
		65,
		channeltypesv2.ErrorAcknowledgement[:],
		channeltypesv2.Payload{},
		sdk.AccAddress{},
	)
	require.NoError(t, err)
	require.Equal(t, providertypes.CONSUMER_PHASE_STOPPED, providerKeeper.GetConsumerPhase(ctx, consumerID))
	require.Equal(t, "65", findEventAttributeValue(ctx.EventManager().Events(), vaastypes.EventTypePacket, "sequence"))
}

func TestIBCModuleOnTimeoutPacketFormatsSequence(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerID := "0"
	clientID := "07-tendermint-0"

	providerKeeper.SetConsumerClientId(ctx, consumerID, clientID)
	providerKeeper.SetConsumerPhase(ctx, consumerID, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerID, "consumer-chain")

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(ctx).Return(21*24*time.Hour, nil).Times(1)

	module := provider.NewIBCModule(&providerKeeper)
	err := module.OnTimeoutPacket(
		ctx,
		clientID,
		"07-tendermint-1",
		42,
		channeltypesv2.Payload{},
		sdk.AccAddress{},
	)
	require.NoError(t, err)
	require.Equal(t, "42", findEventAttributeValue(ctx.EventManager().Events(), vaastypes.EventTypeTimeout, "sequence"))
}

func findEventAttributeValue(events sdk.Events, eventType, key string) string {
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.Type != eventType {
			continue
		}
		for _, attr := range event.Attributes {
			if attr.Key == key {
				return attr.Value
			}
		}
	}
	return ""
}
