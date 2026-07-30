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

// TestIBCModuleOnRecvPacketStoresDestinationClientAsProviderClient guards
// against regressing to storing the packet's SourceClient (the provider's
// own client, meaningless to the consumer for outbound sends) as the
// consumer's ProviderClientID when the bootstrap adoption re-pins from the
// genesis client. It must store DestinationClient: the consumer's own client
// that received the packet, which ibc-go's RecvPacket handler has already
// verified carries a registered counterparty, and which SendEvidencePackets
// later needs to address packets back to the provider.
func TestIBCModuleOnRecvPacketStoresDestinationClientAsProviderClient(t *testing.T) {
	consumerKeeper, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")
	// Start from the genesis placeholder: it has no registered counterparty, so
	// it is the one pin the first delivered packet is allowed to replace. That
	// is what makes which id gets stored observable.
	consumerKeeper.SetProviderClientID(ctx, "07-tendermint-genesis")

	// The unroutable client pinned at genesis; neither the packet's source
	// nor its destination, so storing the wrong one is observable.
	consumerKeeper.SetProviderClientID(ctx, "07-tendermint-9")

	module := consumer.NewIBCModule(&consumerKeeper)

	pk, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	valUpdates := []abci.ValidatorUpdate{{PubKey: pk, Power: 100}}
	vsc := vaastypes.NewValidatorSetChangePacketData(valUpdates, 1)

	payload := channeltypesv2.Payload{
		SourcePort:      vaastypes.ProviderAppID,
		DestinationPort: vaastypes.ConsumerAppID,
		Value:           vsc.GetBytes(),
	}

	const providerOwnClientID = "07-tendermint-0" // packet.SourceClient: the provider's own client
	const consumerOwnClientID = "07-tendermint-1" // packet.DestinationClient: the consumer's own client

	result := module.OnRecvPacket(ctx, providerOwnClientID, consumerOwnClientID, 1, payload, sdk.AccAddress{})
	require.Equal(t, channeltypesv2.PacketStatus_Success, result.Status)

	clientID, found := consumerKeeper.GetProviderClientID(ctx)
	require.True(t, found)
	require.Equal(t, consumerOwnClientID, clientID,
		"ProviderClientID must be the consumer's own (destination) client, not the provider's own (source) client")
}

// TestIBCModuleOnRecvPacketBootstrapReplacesGenesisClient covers the one-time
// bootstrap adoption through the full IBC module callback: the consumer is
// pinned to the genesis-time client (self-created, no registered counterparty,
// unreachable by packet routing), and the first VSC packet delivered over the
// relayer's counterparty-linked client re-pins ProviderClientID to it.
func TestIBCModuleOnRecvPacketBootstrapReplacesGenesisClient(t *testing.T) {
	consumerKeeper, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")
	// Start from the genesis placeholder: it has no registered counterparty, so
	// it is the one pin the first delivered packet is allowed to replace. That
	// is what makes which id gets stored observable.
	consumerKeeper.SetProviderClientID(ctx, "07-tendermint-genesis")

	genesisClientID := "07-tendermint-0"
	consumerKeeper.SetProviderClientID(ctx, genesisClientID)

	module := consumer.NewIBCModule(&consumerKeeper)

	pk, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	valUpdates := []abci.ValidatorUpdate{{PubKey: pk, Power: 100}}
	vsc := vaastypes.NewValidatorSetChangePacketData(valUpdates, 1)

	payload := channeltypesv2.Payload{
		SourcePort:      vaastypes.ProviderAppID,
		DestinationPort: vaastypes.ConsumerAppID,
		Value:           vsc.GetBytes(),
	}

	liveClientID := "07-tendermint-1"
	result := module.OnRecvPacket(ctx, "07-tendermint-0", liveClientID, 1, payload, sdk.AccAddress{})
	require.Equal(t, channeltypesv2.PacketStatus_Success, result.Status)

	clientID, found := consumerKeeper.GetProviderClientID(ctx)
	require.True(t, found)
	require.Equal(t, liveClientID, clientID,
		"ProviderClientID must re-pin to the client actually delivering VSC packets, not stay stuck on the unroutable genesis client")
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
