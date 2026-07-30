package keeper_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	tmtypes "github.com/cometbft/cometbft/types"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	clientv2types "github.com/cosmos/ibc-go/v10/modules/core/02-client/v2/types"
	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	"cosmossdk.io/log"

	sdk "github.com/cosmos/cosmos-sdk/types"

	testcrypto "github.com/allinbits/vaas/testutil/crypto"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
)

// discoveryVal pairs a deterministic crypto identity with a voting power, so
// a test can both store a consumer validator set on the keeper and compute
// the CometBFT hash a genuine consumer chain running that set would carry.
type discoveryVal struct {
	id    *testcrypto.CryptoIdentity
	power int64
}

func discoveryValSet(seedsToPowers map[int]int64) []discoveryVal {
	vals := make([]discoveryVal, 0, len(seedsToPowers))
	for seed, power := range seedsToPowers {
		vals = append(vals, discoveryVal{id: testcrypto.NewCryptoIdentityFromIntSeed(seed), power: power})
	}
	return vals
}

func consensusValidators(vals []discoveryVal) []providertypes.ConsensusValidator {
	out := make([]providertypes.ConsensusValidator, 0, len(vals))
	for _, v := range vals {
		pk := v.id.TMProtoCryptoPublicKey()
		providerAddr := v.id.ProviderConsAddress()
		out = append(out, providertypes.ConsensusValidator{
			ProviderConsAddr: providerAddr.ToSdkConsAddr().Bytes(),
			PublicKey:        &pk,
			Power:            v.power,
		})
	}
	return out
}

// cometValSetHash computes the reference hash independently of the production
// helper (straight through tmtypes), so the tests do not merely compare the
// production code with itself.
func cometValSetHash(vals []discoveryVal) []byte {
	tmVals := make([]*tmtypes.Validator, 0, len(vals))
	for _, v := range vals {
		tmVals = append(tmVals, v.id.TMValidator(v.power))
	}
	return tmtypes.NewValidatorSet(tmVals).Hash()
}

// stubCandidateClient makes the mocked client keepers present clientID as a
// tendermint client of chainID at latestHeight with the given status, a
// registered counterparty, and a latest consensus state carrying
// nextValidatorsHash.
func stubCandidateClient(
	mocks testkeeper.MockedKeepers,
	clientID, chainID string,
	latestHeight clienttypes.Height,
	status ibcexported.Status,
	nextValidatorsHash []byte,
) {
	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), clientID).Return(status).AnyTimes()
	mocks.ClientCounterparties[clientID] = clientv2types.CounterpartyInfo{ClientId: "counterparty-of-" + clientID}
	mocks.MockClientKeeper.EXPECT().GetClientConsensusState(gomock.Any(), clientID, latestHeight).
		DoAndReturn(func(sdk.Context, string, ibcexported.Height) (ibcexported.ConsensusState, bool) {
			return &ibctmtypes.ConsensusState{NextValidatorsHash: nextValidatorsHash}, true
		}).AnyTimes()
}

// stubClientIteration makes IterateClientStates yield exactly the given
// clients, in order.
func stubClientIteration(mocks testkeeper.MockedKeepers, clients map[string]*ibctmtypes.ClientState, order []string) {
	mocks.MockClientKeeper.EXPECT().IterateClientStates(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ sdk.Context, _ []byte, cb func(string, ibcexported.ClientState) bool) {
			for _, clientID := range order {
				if cb(clientID, clients[clientID]) {
					return
				}
			}
		}).AnyTimes()
}

// TestDiscoveryAdoptsContentVerifiedClient covers the adoption happy path: a
// candidate client of the right chain id whose latest consensus state carries
// the hash of the validator set the provider currently has stored for the
// consumer is adopted and persisted.
func TestDiscoveryAdoptsContentVerifiedClient(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerChainId(ctx, consumerId, "consumer-1")

	vals := discoveryValSet(map[int]int64{1: 100, 2: 50})
	require.NoError(t, k.SetConsumerValSet(ctx, consumerId, consensusValidators(vals)))

	clientID := "07-tendermint-3"
	height := clienttypes.NewHeight(1, 42)
	stubCandidateClient(mocks, clientID, "consumer-1", height, ibcexported.Active, cometValSetHash(vals))
	stubClientIteration(mocks, map[string]*ibctmtypes.ClientState{
		clientID: {ChainId: "consumer-1", LatestHeight: height},
	}, []string{clientID})

	got := k.DiscoverActiveConsumerClientForTest(ctx, consumerId, "")
	require.Equal(t, clientID, got)

	stored, found := k.GetConsumerClientId(ctx, consumerId)
	require.True(t, found, "adoption must persist the client id")
	require.Equal(t, clientID, stored)
}

// TestDiscoveryRejectsForgedClient covers the chain-id-collision attack: a
// client that copies the consumer's chain id (and is Active, counterparty-
// linked, and up to date) but tracks a chain run by a different validator set
// must not be adopted, and the look-alike must be logged at warn level.
func TestDiscoveryRejectsForgedClient(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	var logBuf bytes.Buffer
	ctx = ctx.WithLogger(log.NewLogger(&logBuf))

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerChainId(ctx, consumerId, "consumer-1")

	honestVals := discoveryValSet(map[int]int64{1: 100, 2: 50})
	require.NoError(t, k.SetConsumerValSet(ctx, consumerId, consensusValidators(honestVals)))

	// The forged chain is run by the attacker's validators: its (perfectly
	// valid) light client carries their hash, not the one the provider sent.
	attackerVals := discoveryValSet(map[int]int64{666: 100})

	clientID := "07-tendermint-9"
	height := clienttypes.NewHeight(1, 4242)
	stubCandidateClient(mocks, clientID, "consumer-1", height, ibcexported.Active, cometValSetHash(attackerVals))
	stubClientIteration(mocks, map[string]*ibctmtypes.ClientState{
		clientID: {ChainId: "consumer-1", LatestHeight: height},
	}, []string{clientID})

	got := k.DiscoverActiveConsumerClientForTest(ctx, consumerId, "")
	require.Empty(t, got, "a forged client must not be adopted")

	_, found := k.GetConsumerClientId(ctx, consumerId)
	require.False(t, found, "a forged client must not be persisted")

	require.Contains(t, logBuf.String(), "look-alike",
		"a chain-id match failing content verification must be logged")
}

// TestDiscoveryOneStepTolerance verifies a candidate is adopted when its
// consensus state carries the hash of the previously stored validator set:
// the consumer keeps running the previous set until the VSC packet carrying
// the newest one is delivered, which it cannot be before a client is adopted.
func TestDiscoveryOneStepTolerance(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerChainId(ctx, consumerId, "consumer-1")

	launchVals := discoveryValSet(map[int]int64{1: 100, 2: 50})
	rotatedVals := discoveryValSet(map[int]int64{1: 100, 2: 50, 3: 30})
	require.NoError(t, k.SetConsumerValSet(ctx, consumerId, consensusValidators(launchVals)))
	require.NoError(t, k.SetConsumerValSet(ctx, consumerId, consensusValidators(rotatedVals)))

	// The consumer chain is still running the launch set: no VSC has ever
	// been delivered to it.
	clientID := "07-tendermint-3"
	height := clienttypes.NewHeight(1, 42)
	stubCandidateClient(mocks, clientID, "consumer-1", height, ibcexported.Active, cometValSetHash(launchVals))
	stubClientIteration(mocks, map[string]*ibctmtypes.ClientState{
		clientID: {ChainId: "consumer-1", LatestHeight: height},
	}, []string{clientID})

	got := k.DiscoverActiveConsumerClientForTest(ctx, consumerId, "")
	require.Equal(t, clientID, got, "a client carrying the previous set's hash must be adopted")
}

// TestDiscoveryLatchHoldsAcrossClientDeath verifies that once a client has
// been adopted it is returned unconditionally: no status check, no
// counterparty check, no re-discovery. The mocks report the adopted client as
// Expired and counterparty-less -- were the latch to re-inspect or re-discover,
// the unstubbed IterateClientStates expectation would fail the test.
func TestDiscoveryLatchHoldsAcrossClientDeath(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerChainId(ctx, consumerId, "consumer-1")

	adopted := "07-tendermint-0"
	k.SetConsumerClientId(ctx, consumerId, adopted)

	// The adopted client is as dead as a client can be: expired and without a
	// counterparty (ClientCounterparties is left empty). The latch must not care.
	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), adopted).Return(ibcexported.Expired).AnyTimes()

	for range 3 {
		got := k.DiscoverActiveConsumerClientForTest(ctx, consumerId, adopted)
		require.Equal(t, adopted, got, "the adopted client must be returned unconditionally")
	}

	stored, found := k.GetConsumerClientId(ctx, consumerId)
	require.True(t, found)
	require.Equal(t, adopted, stored, "the stored binding must not move")
}

// TestDiscoveryAdoptsNothingWhenNoCandidateVerifies verifies discovery fails
// closed: with no verifying candidate nothing is adopted, and the next call
// retries from scratch -- adopting once a candidate verifies.
func TestDiscoveryAdoptsNothingWhenNoCandidateVerifies(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerChainId(ctx, consumerId, "consumer-1")

	vals := discoveryValSet(map[int]int64{1: 100})
	require.NoError(t, k.SetConsumerValSet(ctx, consumerId, consensusValidators(vals)))

	clientID := "07-tendermint-3"
	height := clienttypes.NewHeight(1, 42)

	// The candidate's consensus state carries a foreign hash at first (e.g.
	// the relayer is mid-setup and the client is not the consumer's), then
	// the genuine one on the next epoch's retry.
	currentHash := cometValSetHash(discoveryValSet(map[int]int64{666: 1}))
	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), clientID).Return(ibcexported.Active).AnyTimes()
	mocks.ClientCounterparties[clientID] = clientv2types.CounterpartyInfo{ClientId: "counterparty"}
	mocks.MockClientKeeper.EXPECT().GetClientConsensusState(gomock.Any(), clientID, height).
		DoAndReturn(func(sdk.Context, string, ibcexported.Height) (ibcexported.ConsensusState, bool) {
			return &ibctmtypes.ConsensusState{NextValidatorsHash: currentHash}, true
		}).AnyTimes()
	stubClientIteration(mocks, map[string]*ibctmtypes.ClientState{
		clientID: {ChainId: "consumer-1", LatestHeight: height},
	}, []string{clientID})

	got := k.DiscoverActiveConsumerClientForTest(ctx, consumerId, "")
	require.Empty(t, got, "no candidate verifies: nothing must be adopted")
	_, found := k.GetConsumerClientId(ctx, consumerId)
	require.False(t, found)

	// Next epoch: the candidate now carries the genuine hash and is adopted.
	currentHash = cometValSetHash(vals)
	got = k.DiscoverActiveConsumerClientForTest(ctx, consumerId, "")
	require.Equal(t, clientID, got, "discovery must retry and adopt once a candidate verifies")
}

// TestDiscoveryPrefersHighestVerifiedHeight verifies the tie-break among
// several verifying candidates: the client with the highest latest height
// (the one a relayer is actively updating) wins.
func TestDiscoveryPrefersHighestVerifiedHeight(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerChainId(ctx, consumerId, "consumer-1")

	vals := discoveryValSet(map[int]int64{1: 100})
	require.NoError(t, k.SetConsumerValSet(ctx, consumerId, consensusValidators(vals)))
	hash := cometValSetHash(vals)

	stale := "07-tendermint-1"
	fresh := "07-tendermint-2"
	staleHeight := clienttypes.NewHeight(1, 10)
	freshHeight := clienttypes.NewHeight(1, 99)
	stubCandidateClient(mocks, stale, "consumer-1", staleHeight, ibcexported.Active, hash)
	stubCandidateClient(mocks, fresh, "consumer-1", freshHeight, ibcexported.Active, hash)
	stubClientIteration(mocks, map[string]*ibctmtypes.ClientState{
		stale: {ChainId: "consumer-1", LatestHeight: staleHeight},
		fresh: {ChainId: "consumer-1", LatestHeight: freshHeight},
	}, []string{stale, fresh})

	got := k.DiscoverActiveConsumerClientForTest(ctx, consumerId, "")
	require.Equal(t, fresh, got)
}

// TestDiscoverySkipsWhenNoValSetHashAvailable verifies discovery fails closed
// when the provider has nothing to verify candidates against (no stored
// validator set and no retained previous hash): it must not even scan for
// candidates, let alone adopt one.
func TestDiscoverySkipsWhenNoValSetHashAvailable(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerChainId(ctx, consumerId, "consumer-1")

	// IterateClientStates is deliberately not stubbed: scanning would fail the test.
	got := k.DiscoverActiveConsumerClientForTest(ctx, consumerId, "")
	require.Empty(t, got)
}

// TestSetConsumerValSetRotatesPrevValSetHash verifies the previous-set hash
// bookkeeping that backs discovery's one-step tolerance: absent before any
// rotation, and always the hash of the set most recently replaced afterwards.
func TestSetConsumerValSetRotatesPrevValSetHash(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := k.FetchAndIncrementConsumerId(ctx)

	setA := discoveryValSet(map[int]int64{1: 100})
	setB := discoveryValSet(map[int]int64{1: 100, 2: 50})
	setC := discoveryValSet(map[int]int64{2: 50})

	// First set ever: there is no previous set, so no hash is retained.
	require.NoError(t, k.SetConsumerValSet(ctx, consumerId, consensusValidators(setA)))
	_, found := k.GetConsumerPrevValSetHash(ctx, consumerId)
	require.False(t, found, "no previous hash may exist before the first rotation")

	// Each rotation retains the hash of the set it replaced.
	require.NoError(t, k.SetConsumerValSet(ctx, consumerId, consensusValidators(setB)))
	got, found := k.GetConsumerPrevValSetHash(ctx, consumerId)
	require.True(t, found)
	require.Equal(t, cometValSetHash(setA), got)

	require.NoError(t, k.SetConsumerValSet(ctx, consumerId, consensusValidators(setC)))
	got, found = k.GetConsumerPrevValSetHash(ctx, consumerId)
	require.True(t, found)
	require.Equal(t, cometValSetHash(setB), got)

	// The production hash helper must agree with the independently computed
	// CometBFT hash, or the content check would never match a real consumer.
	helperHash, err := providerkeeper.ComputeConsumerValSetHash(consensusValidators(setB))
	require.NoError(t, err)
	require.Equal(t, cometValSetHash(setB), helperHash)
}

// TestComputeConsumerValSetHashSkipsZeroPower verifies zero-power entries do
// not contribute to the hash: zero power means "not in the set" throughout
// the protocol, so the consumer's consensus engine never hashes them either.
func TestComputeConsumerValSetHashSkipsZeroPower(t *testing.T) {
	active := discoveryVal{id: testcrypto.NewCryptoIdentityFromIntSeed(1), power: 100}
	removed := discoveryVal{id: testcrypto.NewCryptoIdentityFromIntSeed(2), power: 0}

	withZero, err := providerkeeper.ComputeConsumerValSetHash(consensusValidators([]discoveryVal{active, removed}))
	require.NoError(t, err)
	require.Equal(t, cometValSetHash([]discoveryVal{active}), withZero,
		"a zero-power entry must not change the hash")

	_, err = providerkeeper.ComputeConsumerValSetHash(consensusValidators([]discoveryVal{
		{id: testcrypto.NewCryptoIdentityFromIntSeed(3), power: -1},
	}))
	require.Error(t, err, "a negative power is invalid input, not a removal")
}
