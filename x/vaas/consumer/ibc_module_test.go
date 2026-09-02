package consumer_test

import (
	"strconv"
	"testing"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	"github.com/allinbits/vaas/x/vaas/consumer"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtprotocrypto "github.com/cometbft/cometbft/proto/tendermint/crypto"

	"cosmossdk.io/math"

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
	consumerKeeper, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")
	// Start from the genesis placeholder: it has no registered counterparty, so
	// it is the one pin the first delivered packet is allowed to replace. That
	// is what makes which id gets stored observable.
	consumerKeeper.SetProviderClientID(ctx, "07-tendermint-genesis")

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
	consumerKeeper, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")
	// Start from the genesis placeholder: it has no registered counterparty, so
	// it is the one pin the first delivered packet is allowed to replace. That
	// is what makes which id gets stored observable.
	consumerKeeper.SetProviderClientID(ctx, "07-tendermint-genesis")

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

// TestIBCModuleOnRecvPacketRejectsBadPubkeyInsteadOfPanicking pins the M4 fix:
// a VSC packet carrying a validator update whose consensus pubkey cannot be
// decoded is rejected with an error acknowledgement on receipt, rather than
// being accepted, staged into pending changes, and then panicking the consumer
// at EndBlock when ApplyCCValidatorChanges decodes the pubkey (which would halt
// block production).
func TestIBCModuleOnRecvPacketRejectsBadPubkeyInsteadOfPanicking(t *testing.T) {
	consumerKeeper, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")

	module := consumer.NewIBCModule(&consumerKeeper)

	// An empty PublicKey oneof does not decode to any supported consensus key.
	badUpdate := abci.ValidatorUpdate{PubKey: cmtprotocrypto.PublicKey{}, Power: 5}
	vsc := vaastypes.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{badUpdate}, 1)

	payload := channeltypesv2.Payload{
		SourcePort:      vaastypes.ProviderAppID,
		DestinationPort: vaastypes.ConsumerAppID,
		Value:           vsc.GetBytes(),
	}

	result := module.OnRecvPacket(ctx, "07-tendermint-0", "07-tendermint-1", 1, payload, sdk.AccAddress{})
	require.Equal(t, channeltypesv2.PacketStatus_Failure, result.Status,
		"a VSC packet with an undecodable validator pubkey must be error-acked, not accepted")

	// The bad update never entered pending state, so the deferred EndBlock
	// apply has nothing to choke on.
	_, ok := consumerKeeper.GetPendingChanges(ctx)
	require.False(t, ok, "a rejected packet must not stage any pending validator changes")

	// ApplyCCValidatorChanges is still the defensive backstop: it would panic
	// on the bad update, which is exactly the EndBlock halt the recv-path
	// validation now keeps unreachable.
	require.Panics(t, func() {
		consumerKeeper.ApplyCCValidatorChanges(ctx, []abci.ValidatorUpdate{badUpdate})
	})
}

// TestIBCModuleEvidenceAckAndTimeoutHandling pins the M5 fix: a consumer-sent
// downtime evidence packet is removed from the queue when SendPacket commits
// it, then on the acknowledgement/timeout callback it is either retried or
// permanently discarded. A timeout means non-delivery, so the evidence is
// re-queued and retried. An error acknowledgement is a permanent rejection of
// that exact packet, so it is surfaced as an event and dropped (never
// re-queued, which would loop forever). A success acknowledgement leaves it
// gone. The evidence content is recovered from the callback payload.
func TestIBCModuleEvidenceAckAndTimeoutHandling(t *testing.T) {
	newPacket := func() vaastypes.EvidencePacketData {
		return vaastypes.NewEvidencePacketData(
			sdk.ConsAddress([]byte("consaddr20bytes.....")), 100, []byte{0b00011111}, 8, 600, math.LegacyMustNewDecFromStr("0.5"),
		)
	}
	payloadFor := func(p vaastypes.EvidencePacketData) channeltypesv2.Payload {
		return channeltypesv2.Payload{
			SourcePort:      vaastypes.ConsumerAppID,
			DestinationPort: vaastypes.ProviderAppID,
			Value:           p.GetBytes(),
		}
	}
	rejectionEvents := func(ctx sdk.Context) []sdk.Event {
		var out []sdk.Event
		for _, ev := range ctx.EventManager().Events() {
			if ev.Type == vaastypes.EventTypeConsumerEvidenceRejected {
				out = append(out, ev)
			}
		}
		return out
	}
	attrValue := func(ev sdk.Event, key string) (string, bool) {
		for _, a := range ev.Attributes {
			if a.Key == key {
				return a.Value, true
			}
		}
		return "", false
	}

	t.Run("timeout requeues the evidence", func(t *testing.T) {
		ck, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
		defer ctrl.Finish()
		module := consumer.NewIBCModule(&ck)

		require.Equal(t, 0, ck.GetPendingEvidencePacketCount(ctx))
		require.NoError(t, module.OnTimeoutPacket(ctx, vaastypes.ConsumerAppID, vaastypes.ProviderAppID, 7, payloadFor(newPacket()), sdk.AccAddress{}))
		require.Equal(t, 1, ck.GetPendingEvidencePacketCount(ctx),
			"a timed-out evidence packet must be requeued, not dropped")
	})

	t.Run("error acknowledgement drops the evidence and emits a rejection event", func(t *testing.T) {
		ck, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
		defer ctrl.Finish()
		module := consumer.NewIBCModule(&ck)

		packet := newPacket()
		errAck := channeltypesv2.ErrorAcknowledgement[:]
		require.NoError(t, module.OnAcknowledgementPacket(ctx, vaastypes.ConsumerAppID, vaastypes.ProviderAppID, 7, errAck, payloadFor(packet), sdk.AccAddress{}))

		require.Equal(t, 0, ck.GetPendingEvidencePacketCount(ctx),
			"an error-acked evidence packet is a permanent rejection and must be dropped, not requeued")

		evs := rejectionEvents(ctx)
		require.Len(t, evs, 1, "a rejected evidence packet must surface exactly one rejection event")

		gotAddr, ok := attrValue(evs[0], vaastypes.AttributeValidatorAddress)
		require.True(t, ok)
		require.Equal(t, packet.ValidatorAddr.String(), gotAddr)

		gotWindow, ok := attrValue(evs[0], vaastypes.AttributeWindowEndHeight)
		require.True(t, ok)
		require.Equal(t, strconv.FormatInt(packet.WindowEndHeight, 10), gotWindow)

		// No ack-bytes attribute: an IBC v2 error acknowledgement is a
		// sentinel constant carrying no application error.
		_, ok = attrValue(evs[0], vaastypes.AttributeKeyAckError)
		require.False(t, ok)
	})

	t.Run("success acknowledgement does not requeue or emit a rejection", func(t *testing.T) {
		ck, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
		defer ctrl.Finish()
		module := consumer.NewIBCModule(&ck)

		successAck := []byte{byte(1)}
		require.NoError(t, module.OnAcknowledgementPacket(ctx, vaastypes.ConsumerAppID, vaastypes.ProviderAppID, 7, successAck, payloadFor(newPacket()), sdk.AccAddress{}))
		require.Equal(t, 0, ck.GetPendingEvidencePacketCount(ctx),
			"a success-acked evidence packet must stay removed")
		require.Empty(t, rejectionEvents(ctx), "a success ack must not emit a rejection event")
	})
}
