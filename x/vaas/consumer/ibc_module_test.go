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
// consumer's ProviderClientID. It must store DestinationClient: the
// consumer's own client that received the packet, which ibc-go's RecvPacket
// handler has already verified carries a registered counterparty, and which
// SendEvidencePackets later needs to address packets back to the provider.
func TestIBCModuleOnRecvPacketStoresDestinationClientAsProviderClient(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

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

// TestIBCModuleOnRecvPacketHealsStaleProviderClient guards against the
// consumer latching onto a genesis-time placeholder client (self-created
// before any relayer-established, counterparty-linked client exists) and
// never correcting it: every accepted VSC packet must resync ProviderClientID
// to whichever client actually delivered it.
func TestIBCModuleOnRecvPacketHealsStaleProviderClient(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	staleGenesisClientID := "07-tendermint-0"
	consumerKeeper.SetProviderClientID(ctx, staleGenesisClientID)

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
		"ProviderClientID must heal to the client actually delivering VSC packets, not stay stuck on the stale genesis client")
}
