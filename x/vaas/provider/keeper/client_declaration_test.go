package keeper_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	clientv2types "github.com/cosmos/ibc-go/v10/modules/core/02-client/v2/types"
	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	"github.com/allinbits/vaas/x/vaas/provider/types"
)

// The declaration flow under test: the consumer's owner names the IBC client
// the provider uses to reach the consumer, exactly once, after a relayer has
// created it. Nothing is discovered or adopted: a permissionlessly created
// client proves nothing about which chain it tracks beyond what its own state
// says, so the binding is an owner statement, validated for coherence and
// latched permanently.

const (
	declChainID  = "consumer-decl-1"
	declClientID = "07-tendermint-3"
	declOwner    = "cosmos1dkas8mu4kyhl5jrh4nzvm65qz588hy9qcz08la"
)

type declFixture struct {
	k          providerkeeper.Keeper
	ctx        sdk.Context
	mocks      testkeeper.MockedKeepers
	consumerId uint64
}

// stubClient makes clientID report the given tendermint client state and
// status; counterparty registration is driven by mocks.ClientCounterparties.
func (f *declFixture) stubClient(clientID, chainID string, status ibcexported.Status, trusting time.Duration) {
	f.mocks.MockClientKeeper.EXPECT().
		GetClientState(gomock.Any(), clientID).
		Return(&ibctmtypes.ClientState{
			ChainId:        chainID,
			TrustingPeriod: trusting,
			LatestHeight:   clienttypes.NewHeight(1, 10),
		}, true).AnyTimes()
	f.mocks.MockClientKeeper.EXPECT().
		GetClientStatus(gomock.Any(), clientID).
		Return(status).AnyTimes()
}

func newDeclFixture(t *testing.T) *declFixture {
	t.Helper()
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	t.Cleanup(ctrl.Finish)

	k.SetInfractionParams(ctx, types.DefaultInfractionParameters())
	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerChainId(ctx, consumerId, declChainID)
	k.SetConsumerOwnerAddress(ctx, consumerId, declOwner)
	require.NoError(t, k.SetConsumerInitializationParameters(ctx, consumerId,
		types.ConsumerInitializationParameters{
			SpawnTime:     time.Unix(1_700_000_000, 0).UTC(),
			InitialHeight: clienttypes.NewHeight(1, 1), // chain id ...-1 -> revision 1
		}))

	return &declFixture{k: k, ctx: ctx, mocks: mocks, consumerId: consumerId}
}

// goodTrusting comfortably outlives the default challenge horizon
// (72h evidence age + 7d challenge window).
const goodTrusting = 21 * 24 * time.Hour

func (f *declFixture) declare(t *testing.T, clientID string) error {
	t.Helper()
	msgServer := providerkeeper.NewMsgServerImpl(&f.k)
	_, err := msgServer.UpdateConsumer(f.ctx, &types.MsgUpdateConsumer{
		Owner:      declOwner,
		ConsumerId: f.consumerId,
		ClientId:   clientID,
	})
	return err
}

func TestDeclareConsumerClient(t *testing.T) {
	f := newDeclFixture(t)
	f.stubClient(declClientID, declChainID, ibcexported.Active, goodTrusting)
	f.mocks.ClientCounterparties[declClientID] = clientv2types.CounterpartyInfo{ClientId: "07-tendermint-0"}

	require.NoError(t, f.declare(t, declClientID))

	got, found := f.k.GetConsumerClientId(f.ctx, f.consumerId)
	require.True(t, found)
	require.Equal(t, declClientID, got)
}

func TestDeclareConsumerClientOnlyOnce(t *testing.T) {
	f := newDeclFixture(t)
	f.stubClient(declClientID, declChainID, ibcexported.Active, goodTrusting)
	f.mocks.ClientCounterparties[declClientID] = clientv2types.CounterpartyInfo{ClientId: "07-tendermint-0"}
	require.NoError(t, f.declare(t, declClientID))

	// A second declaration is refused outright, even naming the same client:
	// the binding is permanent, and replacing a dead client is governance's
	// MsgRecoverClient under the same client id, never a re-declaration.
	other := "07-tendermint-9"
	f.stubClient(other, declChainID, ibcexported.Active, goodTrusting)
	f.mocks.ClientCounterparties[other] = clientv2types.CounterpartyInfo{ClientId: "07-tendermint-1"}
	err := f.declare(t, other)
	require.ErrorIs(t, err, types.ErrInvalidMsgUpdateConsumer)

	err = f.declare(t, declClientID)
	require.ErrorIs(t, err, types.ErrInvalidMsgUpdateConsumer)

	got, _ := f.k.GetConsumerClientId(f.ctx, f.consumerId)
	require.Equal(t, declClientID, got, "the original binding must be untouched")
}

func TestDeclareConsumerClientRejectsWrongChainId(t *testing.T) {
	f := newDeclFixture(t)
	f.stubClient(declClientID, "some-other-chain", ibcexported.Active, goodTrusting)
	f.mocks.ClientCounterparties[declClientID] = clientv2types.CounterpartyInfo{ClientId: "07-tendermint-0"}

	err := f.declare(t, declClientID)
	require.ErrorIs(t, err, types.ErrInvalidMsgUpdateConsumer)
	_, found := f.k.GetConsumerClientId(f.ctx, f.consumerId)
	require.False(t, found)
}

func TestDeclareConsumerClientRejectsNonActive(t *testing.T) {
	for _, status := range []ibcexported.Status{ibcexported.Expired, ibcexported.Frozen, ibcexported.Unknown} {
		t.Run(string(status), func(t *testing.T) {
			f := newDeclFixture(t)
			f.stubClient(declClientID, declChainID, status, goodTrusting)
			f.mocks.ClientCounterparties[declClientID] = clientv2types.CounterpartyInfo{ClientId: "07-tendermint-0"}

			err := f.declare(t, declClientID)
			require.ErrorIs(t, err, types.ErrInvalidMsgUpdateConsumer)
		})
	}
}

func TestDeclareConsumerClientRejectsUnroutable(t *testing.T) {
	f := newDeclFixture(t)
	f.stubClient(declClientID, declChainID, ibcexported.Active, goodTrusting)
	// no counterparty registered: packets cannot be routed over it

	err := f.declare(t, declClientID)
	require.ErrorIs(t, err, types.ErrInvalidMsgUpdateConsumer)
}

func TestDeclareConsumerClientRejectsMissingClient(t *testing.T) {
	f := newDeclFixture(t)
	f.mocks.MockClientKeeper.EXPECT().
		GetClientState(gomock.Any(), declClientID).
		Return(nil, false).AnyTimes()

	err := f.declare(t, declClientID)
	require.ErrorIs(t, err, types.ErrInvalidMsgUpdateConsumer)
}

// TestDeclareConsumerClientRejectsShortTrustingPeriod pins the downtime
// falsifiability requirement onto the declaration: every accepted downtime
// accusation must stay disprovable for its whole challenge window, and the
// proof is a header verified against this client. A client whose trusting
// period does not outlive DowntimeEvidenceMaxAge + DowntimeChallengeWindow
// would stop verifying the oldest still-challengeable headers, silently
// converting the optimistic slash into an unconditional one.
func TestDeclareConsumerClientRejectsShortTrustingPeriod(t *testing.T) {
	f := newDeclFixture(t)
	ip := f.k.GetInfractionParams(f.ctx)
	tooShort := ip.DowntimeEvidenceMaxAge + ip.DowntimeChallengeWindow // == horizon: not strictly above
	f.stubClient(declClientID, declChainID, ibcexported.Active, tooShort)
	f.mocks.ClientCounterparties[declClientID] = clientv2types.CounterpartyInfo{ClientId: "07-tendermint-0"}

	err := f.declare(t, declClientID)
	require.ErrorIs(t, err, types.ErrInvalidMsgUpdateConsumer)
}

func TestDeclareConsumerClientOwnerOnly(t *testing.T) {
	f := newDeclFixture(t)
	f.stubClient(declClientID, declChainID, ibcexported.Active, goodTrusting)
	f.mocks.ClientCounterparties[declClientID] = clientv2types.CounterpartyInfo{ClientId: "07-tendermint-0"}

	msgServer := providerkeeper.NewMsgServerImpl(&f.k)
	_, err := msgServer.UpdateConsumer(f.ctx, &types.MsgUpdateConsumer{
		Owner:      "cosmos1qypqxpq9qcrsszgse4wwrq4vt3s2r0y8ryqhx7",
		ConsumerId: f.consumerId,
		ClientId:   declClientID,
	})
	require.ErrorIs(t, err, types.ErrUnauthorized)
}

// TestSendVSCPacketsSkipsUndeclaredConsumer verifies the epoch send path
// leaves an undeclared consumer's packets queued instead of inferring a
// client for it.
func TestSendVSCPacketsSkipsUndeclaredConsumer(t *testing.T) {
	f := newDeclFixture(t)
	// No client declared; SendVSCPackets must be a no-op for this consumer
	// (any attempt to reach IBC would blow up on the unstubbed mock).
	require.NoError(t, f.k.SendVSCPackets(f.ctx))
}

// TestConsumerRegistrationRejectsUnbondingBelowChallengeHorizon pins the other
// half of the falsifiability requirement at the earliest gate: a light client's
// trusting period must be below its chain's unbonding period, so a consumer
// whose unbonding period does not exceed the downtime challenge horizon can
// never have a client that passes the declaration check above. Rejecting the
// registration outright beats letting an unlaunchable consumer be created and
// discovered only when every declaration fails.
func TestConsumerRegistrationRejectsUnbondingBelowChallengeHorizon(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(365*24*time.Hour, nil).AnyTimes()
	k.SetInfractionParams(ctx, types.DefaultInfractionParameters())
	horizon := k.GetInfractionParams(ctx).ChallengeableInterval()

	msgServer := providerkeeper.NewMsgServerImpl(&k)
	_, err := msgServer.CreateConsumer(ctx, &types.MsgCreateConsumer{
		Submitter: declOwner,
		ChainId:   declChainID,
		Metadata:  types.ConsumerMetadata{Name: "n", Description: "d", Metadata: "m"},
		InitializationParameters: &types.ConsumerInitializationParameters{
			SpawnTime:       time.Unix(1_700_000_000, 0).UTC(),
			InitialHeight:   clienttypes.NewHeight(1, 1),
			UnbondingPeriod: horizon, // == horizon: no trusting period below it can exceed it
		},
	})
	require.ErrorIs(t, err, types.ErrInvalidConsumerInitializationParameters)
	require.Contains(t, err.Error(), "unbonding")

	// Comfortably above the horizon: accepted. A fresh chain id, because a
	// keeper-level test has no tx rollback and the rejected create above
	// already registered declChainID before its init params were validated.
	_, err = msgServer.CreateConsumer(ctx, &types.MsgCreateConsumer{
		Submitter: declOwner,
		ChainId:   "consumer-decl-ok-1",
		Metadata:  types.ConsumerMetadata{Name: "n", Description: "d", Metadata: "m"},
		InitializationParameters: &types.ConsumerInitializationParameters{
			SpawnTime:       time.Unix(1_700_000_000, 0).UTC(),
			InitialHeight:   clienttypes.NewHeight(1, 1),
			UnbondingPeriod: 3 * horizon,
		},
	})
	require.NoError(t, err)
}

// TestMakeConsumerGenesisSeedsOwnerAddress pins the provider half of the
// consumer-side pin authority: the registered owner must reach the consumer
// genesis params, because it is the account MsgSetProviderClient authorizes
// on the consumer at bootstrap. An empty seed would leave the consumer's pin
// to governance alone.
func TestMakeConsumerGenesisSeedsOwnerAddress(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	k.SetInfractionParams(ctx, types.DefaultInfractionParameters())
	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerChainId(ctx, consumerId, declChainID)
	k.SetConsumerOwnerAddress(ctx, consumerId, declOwner)
	require.NoError(t, k.SetConsumerInitializationParameters(ctx, consumerId,
		types.ConsumerInitializationParameters{
			SpawnTime:       time.Unix(1_700_000_000, 0).UTC(),
			InitialHeight:   clienttypes.NewHeight(1, 1),
			UnbondingPeriod: 21 * 24 * time.Hour,
		}))

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(28*24*time.Hour, nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().GetHistoricalInfo(gomock.Any(), gomock.Any()).
		Return(stakingtypes.HistoricalInfo{Header: cmtproto.Header{
			Time:               time.Unix(1_700_000_000, 0).UTC(),
			AppHash:            []byte("apphash"),
			NextValidatorsHash: []byte("next_vals_hash"),
		}}, nil).AnyTimes()

	gen, err := k.MakeConsumerGenesis(ctx, consumerId, nil)
	require.NoError(t, err)
	require.Equal(t, declOwner, gen.Params.OwnerAddress,
		"the registered owner must be seeded as the consumer-side pin authority")
}
