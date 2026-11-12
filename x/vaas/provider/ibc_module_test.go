package provider_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	ibcclienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	"github.com/allinbits/vaas/x/vaas/provider"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
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
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerID := uint64(0)
	clientID := "07-tendermint-0"

	providerKeeper.SetConsumerClientId(ctx, consumerID, clientID)
	providerKeeper.SetConsumerPhase(ctx, consumerID, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerID, "consumer-chain")

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
	// Consumer stays launched; liveness sweep owns removal
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, providerKeeper.GetConsumerPhase(ctx, consumerID))
	require.Equal(t, "65", findEventAttributeValue(ctx.EventManager().Events(), vaastypes.EventTypePacket, "sequence"))
}

func TestIBCModuleOnTimeoutPacketFormatsSequence(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerID := uint64(0)
	clientID := "07-tendermint-0"

	providerKeeper.SetConsumerClientId(ctx, consumerID, clientID)
	providerKeeper.SetConsumerPhase(ctx, consumerID, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerID, "consumer-chain")

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

// declaredClientID is the provider's client of the consumer in the evidence
// fixture, the one the consumer's owner declared.
const declaredClientID = "07-tendermint-0"

// evidenceFixture is a launched consumer, declared under declaredClientID,
// with one validator in its set whom evidencePayload accuses of missing 6 of
// 8 blocks over heights 93 to 100. The window-end anchor is overridden and
// an epoch share record seeded so the accusation, delivered at the window
// end, lands inside every acceptance bound and prices to a pending slash.
type evidenceFixture struct {
	keeper     providerkeeper.Keeper
	ctx        sdk.Context
	mocks      testkeeper.MockedKeepers
	consumerID uint64
	consAddr   sdk.ConsAddress
}

func newEvidenceFixture(t *testing.T) (evidenceFixture, *gomock.Controller) {
	t.Helper()
	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	ctx = ctx.WithBlockTime(windowEndTime)

	const consumerID = uint64(0)
	providerKeeper.SetConsumerPhase(ctx, consumerID, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerID, "consumer-chain")
	providerKeeper.SetConsumerClientId(ctx, consumerID, declaredClientID)
	providerKeeper.SetEquivocationEvidenceMinHeight(ctx, consumerID, 1)
	providerKeeper.SetInfractionParams(ctx, providertypes.InfractionParameters{
		SignedBlocksWindow:      8,
		MinSignedPerWindow:      math.LegacyMustNewDecFromStr("0.5"),
		DowntimeChallengeWindow: 7 * 24 * time.Hour,
		DowntimeEvidenceMaxAge:  72 * time.Hour,
	})
	require.NoError(t, providerKeeper.SetConsumerInitializationParameters(ctx, consumerID, providertypes.ConsumerInitializationParameters{
		SpawnTime: windowEndTime.Add(-30 * 24 * time.Hour),
	}))

	pubKey := ed25519.GenPrivKey().PubKey()
	consAddr := sdk.ConsAddress(pubKey.Address())
	cmtPubKey, err := cryptocodec.ToCmtProtoPublicKey(pubKey)
	require.NoError(t, err)
	require.NoError(t, providerKeeper.SetConsumerValidator(ctx, consumerID, providertypes.ConsensusValidator{
		ProviderConsAddr: consAddr,
		Power:            1000,
		PublicKey:        &cmtPubKey,
		JoinHeight:       1,
	}))

	// The client store holds a consensus state at exactly the window end, so
	// the real anchor resolution lands the window at windowEndTime, and one
	// below the window start (93), so the window's whole span lies inside the
	// states the client retains. The client state tracks the consumer's chain.
	storeProvider := ibcclienttypes.NewStoreProvider(runtime.NewKVStoreService(keeperParams.StoreKey))
	clientStore := storeProvider.ClientStore(ctx, declaredClientID)
	belowWindowStartHeight := ibcclienttypes.NewHeight(0, 90)
	windowEndHeight := ibcclienttypes.NewHeight(0, 100)
	ibctmtypes.SetIterationKey(clientStore, belowWindowStartHeight)
	ibctmtypes.SetIterationKey(clientStore, windowEndHeight)
	mocks.MockClientKeeper.EXPECT().GetStoreProvider().Return(storeProvider).AnyTimes()
	mocks.MockClientKeeper.EXPECT().GetClientConsensusState(gomock.Any(), declaredClientID, belowWindowStartHeight).
		Return(&ibctmtypes.ConsensusState{Timestamp: windowEndTime.Add(-time.Minute)}, true).AnyTimes()
	mocks.MockClientKeeper.EXPECT().GetClientConsensusState(gomock.Any(), declaredClientID, windowEndHeight).
		Return(&ibctmtypes.ConsensusState{Timestamp: windowEndTime}, true).AnyTimes()
	mocks.MockClientKeeper.EXPECT().GetClientState(gomock.Any(), declaredClientID).
		Return(&ibctmtypes.ClientState{ChainId: "consumer-chain", TrustingPeriod: 365 * 24 * time.Hour}, true).AnyTimes()
	providerKeeper.SetEpochShareRecord(ctx, consumerID, windowEndTime, math.NewInt(1000))

	return evidenceFixture{
		keeper:     providerKeeper,
		ctx:        ctx,
		mocks:      mocks,
		consumerID: consumerID,
		consAddr:   consAddr,
	}, ctrl
}

func (f evidenceFixture) evidencePayload() channeltypesv2.Payload {
	packet := vaastypes.NewEvidencePacketData(f.consAddr, 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	return channeltypesv2.Payload{
		SourcePort:      vaastypes.ConsumerAppID,
		DestinationPort: vaastypes.ProviderAppID,
		Value:           packet.GetBytes(),
	}
}

// hasPendingSlash reports whether the accusation evidencePayload carries was
// queued against the fixture's validator.
func (f evidenceFixture) hasPendingSlash(t *testing.T) bool {
	t.Helper()
	_, err := f.keeper.PendingDowntimeSlashes.Get(f.ctx, collections.Join3(f.consumerID, f.consAddr.Bytes(), int64(100)))
	if errors.Is(err, collections.ErrNotFound) {
		return false
	}
	require.NoError(t, err)
	return true
}

// TestIBCModuleOnRecvPacketAcceptsEvidenceOverTheDeclaredClient: an evidence
// packet delivered over the client the consumer's owner declared is handed
// to the consumer that client maps to, queued as a priced downtime slash,
// and acknowledged with the success sentinel.
func TestIBCModuleOnRecvPacketAcceptsEvidenceOverTheDeclaredClient(t *testing.T) {
	f, ctrl := newEvidenceFixture(t)
	defer ctrl.Finish()
	f.mocks.MockPhotonKeeper.EXPECT().ConversionRate(gomock.Any()).Return(math.LegacyNewDec(2), nil)

	module := provider.NewIBCModule(&f.keeper)
	result := module.OnRecvPacket(f.ctx, "07-tendermint-1", declaredClientID, 1, f.evidencePayload(), sdk.AccAddress{})
	require.Equal(t, channeltypesv2.PacketStatus_Success, result.Status)
	require.Equal(t, []byte{byte(1)}, result.Acknowledgement)
	require.True(t, f.hasPendingSlash(t), "an accepted accusation is queued behind the challenge window")
}

// TestIBCModuleOnRecvPacketRejectsEvidenceFromAnUndeclaredClient: the same
// packet delivered over a client no consumer's owner declared is refused
// with a failure ack, and nothing is queued. Any account can create an IBC
// v2 client and relay packets through it, so the destination client proves
// a packet came from a consumer only once a declaration maps it to one. No
// pricing is mocked, so the controller also proves the handler never ran.
func TestIBCModuleOnRecvPacketRejectsEvidenceFromAnUndeclaredClient(t *testing.T) {
	f, ctrl := newEvidenceFixture(t)
	defer ctrl.Finish()

	module := provider.NewIBCModule(&f.keeper)
	result := module.OnRecvPacket(f.ctx, "07-tendermint-1", "07-tendermint-7", 1, f.evidencePayload(), sdk.AccAddress{})
	require.Equal(t, channeltypesv2.PacketStatus_Failure, result.Status)
	require.False(t, f.hasPendingSlash(t), "an undeclared client must not reach any consumer's evidence handler")
}

// TestIBCModuleOnRecvPacketFailureAcksWhatTheHandlerNeverAccepts: over the
// declared client, a payload that is not an evidence packet, a packet on the
// wrong ports, and a well-formed packet the handler refuses (the consumer is
// paused) all end in a failure ack with nothing queued.
func TestIBCModuleOnRecvPacketFailureAcksWhatTheHandlerNeverAccepts(t *testing.T) {
	cases := []struct {
		name     string
		malleate func(f *evidenceFixture, payload *channeltypesv2.Payload)
	}{
		{"malformed payload", func(_ *evidenceFixture, payload *channeltypesv2.Payload) {
			payload.Value = []byte("not an evidence packet")
		}},
		{"wrong destination port", func(_ *evidenceFixture, payload *channeltypesv2.Payload) {
			payload.DestinationPort = vaastypes.ConsumerAppID
		}},
		{"wrong source port", func(_ *evidenceFixture, payload *channeltypesv2.Payload) {
			payload.SourcePort = vaastypes.ProviderAppID
		}},
		{"handler refuses a paused consumer", func(f *evidenceFixture, _ *channeltypesv2.Payload) {
			f.keeper.SetConsumerPhase(f.ctx, f.consumerID, providertypes.CONSUMER_PHASE_PAUSED)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, ctrl := newEvidenceFixture(t)
			defer ctrl.Finish()
			payload := f.evidencePayload()
			tc.malleate(&f, &payload)

			module := provider.NewIBCModule(&f.keeper)
			result := module.OnRecvPacket(f.ctx, "07-tendermint-1", declaredClientID, 1, payload, sdk.AccAddress{})
			require.Equal(t, channeltypesv2.PacketStatus_Failure, result.Status)
			require.False(t, f.hasPendingSlash(t))
		})
	}
}
