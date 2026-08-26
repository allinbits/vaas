package consumer_test

import (
	"testing"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	"github.com/allinbits/vaas/x/vaas/consumer"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"
)

// TestIBCModuleOnRecvPacketMatchesPinAgainstDestinationClient guards against
// regressing to comparing the packet's SourceClient (the provider's own
// client, meaningless on the consumer) against the provider-client pin. The
// pin names the consumer's OWN client of the provider, so it is the packet's
// DestinationClient that must match it: a packet delivered over the pinned
// client is accepted, and the same packet is rejected when only its
// SourceClient happens to equal the pin.
func TestIBCModuleOnRecvPacketMatchesPinAgainstDestinationClient(t *testing.T) {
	const providerOwnClientID = "07-tendermint-0" // packet.SourceClient: the provider's own client
	const consumerOwnClientID = "07-tendermint-1" // packet.DestinationClient: the consumer's own client

	newPacket := func() channeltypesv2.Payload {
		pk, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
		require.NoError(t, err)
		vsc := vaastypes.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: pk, Power: 100}}, 1)
		return channeltypesv2.Payload{
			SourcePort:      vaastypes.ProviderAppID,
			DestinationPort: vaastypes.ConsumerAppID,
			Value:           vsc.GetBytes(),
		}
	}

	t.Run("packet over the pinned (destination) client is accepted", func(t *testing.T) {
		consumerKeeper, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
		defer ctrl.Finish()
		testkeeper.StubClientState(mocks, "provider-0")
		consumerKeeper.SetProviderClientID(ctx, consumerOwnClientID)

		module := consumer.NewIBCModule(&consumerKeeper)
		result := module.OnRecvPacket(ctx, providerOwnClientID, consumerOwnClientID, 1, newPacket(), sdk.AccAddress{})
		require.Equal(t, channeltypesv2.PacketStatus_Success, result.Status)
	})

	t.Run("packet whose source client merely equals the pin is rejected", func(t *testing.T) {
		consumerKeeper, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
		defer ctrl.Finish()
		testkeeper.StubClientState(mocks, "provider-0")
		consumerKeeper.SetProviderClientID(ctx, providerOwnClientID)

		module := consumer.NewIBCModule(&consumerKeeper)
		result := module.OnRecvPacket(ctx, providerOwnClientID, consumerOwnClientID, 1, newPacket(), sdk.AccAddress{})
		require.Equal(t, channeltypesv2.PacketStatus_Failure, result.Status,
			"the pin names the consumer's own client, so only DestinationClient may satisfy it")
	})
}

// TestIBCModuleOnRecvPacketRejectsWhileUnpinned covers the bootstrap surface
// through the full IBC module callback: with no provider client pinned -- the
// state every new chain starts in, since genesis creates no client -- a VSC
// packet is error-acked, whatever client delivers it. The pin only ever comes
// from MsgSetProviderClient.
func TestIBCModuleOnRecvPacketRejectsWhileUnpinned(t *testing.T) {
	consumerKeeper, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")

	module := consumer.NewIBCModule(&consumerKeeper)

	pk, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	vsc := vaastypes.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: pk, Power: 100}}, 1)
	payload := channeltypesv2.Payload{
		SourcePort:      vaastypes.ProviderAppID,
		DestinationPort: vaastypes.ConsumerAppID,
		Value:           vsc.GetBytes(),
	}

	result := module.OnRecvPacket(ctx, "07-tendermint-0", "07-tendermint-1", 1, payload, sdk.AccAddress{})
	require.Equal(t, channeltypesv2.PacketStatus_Failure, result.Status)

	_, found := consumerKeeper.GetProviderClientID(ctx)
	require.False(t, found, "a rejected packet must not establish the pin")
}

// TestIBCModuleOnRecvPacketRejectsWrongSourcePort guards against accepting a
// VSC-shaped packet whose SourcePort isn't the provider's app ID. Without
// this check, a payload merely matching the consumer's own DestinationPort
// would be handled as if it came from the provider, no matter which module
// on the counterparty chain actually sent it.
func TestIBCModuleOnRecvPacketRejectsWrongSourcePort(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	module := consumer.NewIBCModule(&consumerKeeper)

	pk, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	valUpdates := []abci.ValidatorUpdate{{PubKey: pk, Power: 100}}
	vsc := vaastypes.NewValidatorSetChangePacketData(valUpdates, 1)

	payload := channeltypesv2.Payload{
		SourcePort:      "not-" + vaastypes.ProviderAppID,
		DestinationPort: vaastypes.ConsumerAppID,
		Value:           vsc.GetBytes(),
	}

	result := module.OnRecvPacket(ctx, "07-tendermint-0", "07-tendermint-1", 1, payload, sdk.AccAddress{})
	require.Equal(t, channeltypesv2.PacketStatus_Failure, result.Status,
		"a packet whose SourcePort isn't the provider's app ID must be rejected")

	_, found := consumerKeeper.GetProviderClientID(ctx)
	require.False(t, found, "a rejected packet must not establish the provider client")
}
