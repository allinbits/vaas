package keeper_test

import (
	"bytes"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	protov2 "google.golang.org/protobuf/proto"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	abci "github.com/cometbft/cometbft/abci/types"
	tmprotocrypto "github.com/cometbft/cometbft/proto/tendermint/crypto"

	cryptotestutil "github.com/allinbits/vaas/testutil/crypto"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providerante "github.com/allinbits/vaas/x/vaas/provider/ante"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// rotationTx is a minimal sdk.Tx carrying consensus-key rotation messages, for
// driving the provider ante decorator against real keeper state.
type rotationTx struct {
	msgs []sdk.Msg
}

func (tx rotationTx) GetMsgs() []sdk.Msg                    { return tx.msgs }
func (tx rotationTx) GetMsgsV2() ([]protov2.Message, error) { return nil, nil }

func TestValidatorConsumerPubKeyCRUD(t *testing.T) {
	consumerID := CONSUMER_ID
	providerAddr := types.NewProviderConsAddress([]byte("providerAddr"))
	consumerKey := cryptotestutil.NewCryptoIdentityFromIntSeed(1).TMProtoCryptoPublicKey()

	keeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	keeper.SetValidatorConsumerPubKey(ctx, consumerID, providerAddr, consumerKey)

	consumerPubKey, found := keeper.GetValidatorConsumerPubKey(ctx, consumerID, providerAddr)
	require.True(t, found, "consumer pubkey not found")
	require.NotEmpty(t, consumerPubKey, "consumer pubkey is empty")
	require.Equal(t, consumerPubKey, consumerKey)

	keeper.DeleteValidatorConsumerPubKey(ctx, consumerID, providerAddr)
	consumerPubKey, found = keeper.GetValidatorConsumerPubKey(ctx, consumerID, providerAddr)
	require.False(t, found, "consumer pubkey was found")
	require.Empty(t, consumerPubKey, "consumer pubkey was returned")
	require.NotEqual(t, consumerPubKey, consumerKey)
}

// TestMigrateStateOnConsPubKeyRotationCoversEveryConsumerAndMapping checks that
// a provider consensus-key rotation moves a validator's assigned-key state from
// its old provider consensus address to its new one, so the assigned key keeps
// resolving (VSC set computation) and evidence keeps attributing (consumer ->
// provider) -- on every consumer the validator has an assignment on, and for
// every reverse mapping that names the old address, not just the first of
// either.
//
// Both counts are load-bearing. A validator validates every consumer, so a
// migration that stopped at the first would leave the rest resolving a rotated
// validator's assigned key under an address it no longer holds. And a validator
// has more than one reverse mapping on a consumer whenever it has re-assigned
// its consumer key there: the superseded consumer addresses are deliberately
// kept resolvable until the unbonding period elapses, precisely so a slash
// request naming one can still be attributed, so a migration that stopped at the
// first repoint would orphan exactly the mappings that exist for pending
// slashes.
func TestMigrateStateOnConsPubKeyRotationCoversEveryConsumerAndMapping(t *testing.T) {
	keeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	rotating := cryptotestutil.NewCryptoIdentityFromIntSeed(1)
	rotated := cryptotestutil.NewCryptoIdentityFromIntSeed(2)
	oldProviderAddr := rotating.ProviderConsAddress()
	newProviderAddr := rotated.ProviderConsAddress()

	// A bystander validator with its own assignment on every consumer, to pin
	// that the migration repoints the rotating validator's mappings only.
	bystanderAddr := cryptotestutil.NewCryptoIdentityFromIntSeed(3).ProviderConsAddress()

	// Three consumers, each holding: the rotating validator's current assigned
	// key, two superseded consumer addresses still pointing at its old provider
	// address, and the bystander's assignment.
	type consumerState struct {
		id            uint64
		consumerKey   tmprotocrypto.PublicKey
		consumerAddr  types.ConsumerConsAddress
		supersededOne types.ConsumerConsAddress
		supersededTwo types.ConsumerConsAddress
		bystanderAddr types.ConsumerConsAddress
	}
	consumers := []consumerState{}
	for i := range 3 {
		seed := 10 + i*10
		current := cryptotestutil.NewCryptoIdentityFromIntSeed(seed)
		state := consumerState{
			id:            keeper.FetchAndIncrementConsumerId(ctx),
			consumerKey:   current.TMProtoCryptoPublicKey(),
			consumerAddr:  current.ConsumerConsAddress(),
			supersededOne: cryptotestutil.NewCryptoIdentityFromIntSeed(seed + 1).ConsumerConsAddress(),
			supersededTwo: cryptotestutil.NewCryptoIdentityFromIntSeed(seed + 2).ConsumerConsAddress(),
			bystanderAddr: cryptotestutil.NewCryptoIdentityFromIntSeed(seed + 3).ConsumerConsAddress(),
		}
		keeper.SetConsumerPhase(ctx, state.id, types.CONSUMER_PHASE_LAUNCHED)
		keeper.SetValidatorConsumerPubKey(ctx, state.id, oldProviderAddr, state.consumerKey)
		keeper.SetValidatorByConsumerAddr(ctx, state.id, state.consumerAddr, oldProviderAddr)
		keeper.SetValidatorByConsumerAddr(ctx, state.id, state.supersededOne, oldProviderAddr)
		keeper.SetValidatorByConsumerAddr(ctx, state.id, state.supersededTwo, oldProviderAddr)
		keeper.SetValidatorByConsumerAddr(ctx, state.id, state.bystanderAddr, bystanderAddr)
		consumers = append(consumers, state)
	}

	keeper.MigrateStateOnConsPubKeyRotation(ctx, oldProviderAddr, newProviderAddr)

	for _, state := range consumers {
		// The assigned key resolves under the new provider address, not the old.
		gotKey, found := keeper.GetValidatorConsumerPubKey(ctx, state.id, newProviderAddr)
		require.True(t, found, "assigned key should resolve under the rotated provider address on consumer %d", state.id)
		require.Equal(t, state.consumerKey, gotKey)
		_, found = keeper.GetValidatorConsumerPubKey(ctx, state.id, oldProviderAddr)
		require.False(t, found, "assigned key should not survive under the old provider address on consumer %d", state.id)

		// Every reverse mapping the rotating validator owns resolves to the new
		// address: the current assignment and both superseded consumer addresses.
		for name, consumerAddr := range map[string]types.ConsumerConsAddress{
			"current":       state.consumerAddr,
			"superseded #1": state.supersededOne,
			"superseded #2": state.supersededTwo,
		} {
			require.Equal(t, newProviderAddr, keeper.GetProviderAddrFromConsumerAddr(ctx, state.id, consumerAddr),
				"%s consumer address should attribute to the rotated provider address on consumer %d", name, state.id)
		}

		// The bystander's mapping is untouched.
		gotProviderAddr, found := keeper.GetValidatorByConsumerAddr(ctx, state.id, state.bystanderAddr)
		require.True(t, found)
		require.Equal(t, bystanderAddr, gotProviderAddr,
			"another validator's mapping must not be repointed on consumer %d", state.id)
	}
}

// TestAfterConsensusPubKeyUpdateNeverErrorsOnCollision pins the hook's contract.
// It fires in EndBlock, from x/staking's ApplyAndReturnValidatorSetUpdates, once
// the rotation is already committed: an error returned there propagates out of
// EndBlock and halts the provider chain, and any bonded validator could trigger
// it by rotating onto a public consumer key. So even a rotation onto a key
// already assigned as another validator's consumer key -- which the hook can
// observe but no longer prevent -- must return nil and still migrate the
// rotating validator's own assignment state. Admission-time rejection lives in
// the provider ante decorator instead.
func TestAfterConsensusPubKeyUpdateNeverErrorsOnCollision(t *testing.T) {
	keeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerID := keeper.FetchAndIncrementConsumerId(ctx)
	keeper.SetConsumerPhase(ctx, consumerID, types.CONSUMER_PHASE_LAUNCHED)

	// Validator B has assigned consumer key K on the consumer.
	victimKey := cryptotestutil.NewCryptoIdentityFromIntSeed(1)
	bProviderAddr := cryptotestutil.NewCryptoIdentityFromIntSeed(5).ProviderConsAddress()
	keeper.SetValidatorByConsumerAddr(ctx, consumerID, victimKey.ConsumerConsAddress(), bProviderAddr)

	// Validator A holds an assignment of its own, and rotates its provider
	// consensus key onto K (the rotation message carries no proof of possession).
	aOld := cryptotestutil.NewCryptoIdentityFromIntSeed(2)
	aConsumerKey := cryptotestutil.NewCryptoIdentityFromIntSeed(6)
	keeper.SetValidatorConsumerPubKey(ctx, consumerID, aOld.ProviderConsAddress(), aConsumerKey.TMProtoCryptoPublicKey())
	keeper.SetValidatorByConsumerAddr(ctx, consumerID, aConsumerKey.ConsumerConsAddress(), aOld.ProviderConsAddress())

	err := keeper.Hooks().AfterConsensusPubKeyUpdate(
		ctx, aOld.ConsensusSDKPubKey(), victimKey.ConsensusSDKPubKey(), sdk.Coin{},
	)
	require.NoError(t, err,
		"the hook runs in EndBlock: returning an error would halt the provider chain")

	// A's own assignment still migrated to its rotated provider address.
	aNewAddr := types.NewProviderConsAddress(victimKey.SDKValConsAddress())
	gotKey, found := keeper.GetValidatorConsumerPubKey(ctx, consumerID, aNewAddr)
	require.True(t, found, "the rotating validator's assignment must still migrate")
	require.Equal(t, aConsumerKey.TMProtoCryptoPublicKey(), gotKey)
	_, found = keeper.GetValidatorConsumerPubKey(ctx, consumerID, aOld.ProviderConsAddress())
	require.False(t, found, "stale assignment must not survive under the old address")
	require.Equal(t, aNewAddr,
		keeper.GetProviderAddrFromConsumerAddr(ctx, consumerID, aConsumerKey.ConsumerConsAddress()))

	// B's assignment is not touched: the migration only repoints A's own state.
	gotProviderAddr, found := keeper.GetValidatorByConsumerAddr(ctx, consumerID, victimKey.ConsumerConsAddress())
	require.True(t, found)
	require.Equal(t, bProviderAddr, gotProviderAddr)
}

// TestConsumerKeyGuardsCoverPausedConsumers: a paused consumer keeps all of its
// key assignments and can be resumed, so both the admission-time collision check
// and the rotation migration must see it. Missing it would let a second validator
// take a paused consumer's assigned consumer key as its own provider key, putting
// two validators at one consensus address on that consumer the moment it resumes.
func TestConsumerKeyGuardsCoverPausedConsumers(t *testing.T) {
	keeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerID := keeper.FetchAndIncrementConsumerId(ctx)
	keeper.SetConsumerPhase(ctx, consumerID, types.CONSUMER_PHASE_PAUSED)

	victimKey := cryptotestutil.NewCryptoIdentityFromIntSeed(11)
	holderOld := cryptotestutil.NewCryptoIdentityFromIntSeed(12)
	holderNew := cryptotestutil.NewCryptoIdentityFromIntSeed(13)
	holderAddr := holderOld.ProviderConsAddress()

	keeper.SetValidatorConsumerPubKey(ctx, consumerID, holderAddr, victimKey.TMProtoCryptoPublicKey())
	keeper.SetValidatorByConsumerAddr(ctx, consumerID, victimKey.ConsumerConsAddress(), holderAddr)

	// The ante decorator must reject a rotation onto the paused consumer's
	// assigned key, reading the real keeper state.
	pkAny, err := codectypes.NewAnyWithValue(victimKey.ConsensusSDKPubKey())
	require.NoError(t, err)
	rotation := &stakingtypes.MsgRotateConsPubKey{
		ValidatorAddress: cryptotestutil.NewCryptoIdentityFromIntSeed(14).SDKValOpAddressString(),
		NewPubkey:        pkAny,
	}
	_, err = providerante.NewConsPubKeyRotationDecorator(keeper).AnteHandle(
		ctx, rotationTx{msgs: []sdk.Msg{rotation}}, false,
		func(ctx sdk.Context, _ sdk.Tx, _ bool) (sdk.Context, error) { return ctx, nil },
	)
	require.ErrorIs(t, err, types.ErrConsumerKeyInUse,
		"a paused consumer's assigned key must still be treated as in use")

	// The migration must follow a rotation on the paused consumer too.
	keeper.MigrateStateOnConsPubKeyRotation(ctx, holderAddr, holderNew.ProviderConsAddress())

	gotKey, found := keeper.GetValidatorConsumerPubKey(ctx, consumerID, holderNew.ProviderConsAddress())
	require.True(t, found, "assignment must migrate for a paused consumer")
	require.Equal(t, victimKey.TMProtoCryptoPublicKey(), gotKey)
	_, found = keeper.GetValidatorConsumerPubKey(ctx, consumerID, holderAddr)
	require.False(t, found, "stale assignment must not survive under the old address")
	require.Equal(t, holderNew.ProviderConsAddress(),
		keeper.GetProviderAddrFromConsumerAddr(ctx, consumerID, victimKey.ConsumerConsAddress()))
}

// TestAfterConsensusPubKeyUpdateMigratesAssignment exercises the full hook on a
// legitimate rotation (onto a fresh key): the validator's assigned-key state
// follows to the new provider address.
func TestAfterConsensusPubKeyUpdateMigratesAssignment(t *testing.T) {
	keeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerID := keeper.FetchAndIncrementConsumerId(ctx)
	keeper.SetConsumerPhase(ctx, consumerID, types.CONSUMER_PHASE_LAUNCHED)

	aOld := cryptotestutil.NewCryptoIdentityFromIntSeed(2) // A's old provider key
	aNew := cryptotestutil.NewCryptoIdentityFromIntSeed(3) // fresh rotation target
	cKey := cryptotestutil.NewCryptoIdentityFromIntSeed(4) // A's assigned consumer key

	oldAddr := aOld.ProviderConsAddress()
	newAddr := aNew.ProviderConsAddress()
	consumerKey := cKey.TMProtoCryptoPublicKey()
	consumerAddr := cKey.ConsumerConsAddress()

	keeper.SetValidatorConsumerPubKey(ctx, consumerID, oldAddr, consumerKey)
	keeper.SetValidatorByConsumerAddr(ctx, consumerID, consumerAddr, oldAddr)

	err := keeper.Hooks().AfterConsensusPubKeyUpdate(ctx, aOld.ConsensusSDKPubKey(), aNew.ConsensusSDKPubKey(), sdk.Coin{})
	require.NoError(t, err)

	gotKey, found := keeper.GetValidatorConsumerPubKey(ctx, consumerID, newAddr)
	require.True(t, found)
	require.Equal(t, consumerKey, gotKey)
	_, found = keeper.GetValidatorConsumerPubKey(ctx, consumerID, oldAddr)
	require.False(t, found)
	require.Equal(t, newAddr, keeper.GetProviderAddrFromConsumerAddr(ctx, consumerID, consumerAddr))
}

func TestGetAllValidatorConsumerPubKey(t *testing.T) {
	pk, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	seed := time.Now().UnixNano()
	rng := rand.New(rand.NewSource(seed))

	consumerIDs := []uint64{1, 2, 3}
	numAssignments := 10
	testAssignments := []types.ValidatorConsumerPubKey{}
	for i := range numAssignments {
		consumerKey := cryptotestutil.NewCryptoIdentityFromIntSeed(i).TMProtoCryptoPublicKey()
		providerAddr := cryptotestutil.NewCryptoIdentityFromIntSeed(numAssignments + i).ProviderConsAddress()
		testAssignments = append(testAssignments,
			types.ValidatorConsumerPubKey{
				ConsumerId:   consumerIDs[rng.Intn(len(consumerIDs))],
				ProviderAddr: providerAddr.ToSdkConsAddr(),
				ConsumerKey:  &consumerKey,
			},
		)
	}
	// select a consumerId with more than two assignments
	var consumerID uint64
	for i := range consumerIDs {
		consumerID = consumerIDs[i]
		count := 0
		for _, assignment := range testAssignments {
			if assignment.ConsumerId == consumerID {
				count++
			}
		}
		if count > 2 {
			break
		}
	}
	expectedGetAllOneConsumerOrder := []types.ValidatorConsumerPubKey{}
	for _, assignment := range testAssignments {
		if assignment.ConsumerId == consumerID {
			expectedGetAllOneConsumerOrder = append(expectedGetAllOneConsumerOrder, assignment)
		}
	}
	// sorting by ValidatorConsumerPubKey.ProviderAddr
	sort.Slice(expectedGetAllOneConsumerOrder, func(i, j int) bool {
		return bytes.Compare(expectedGetAllOneConsumerOrder[i].ProviderAddr, expectedGetAllOneConsumerOrder[j].ProviderAddr) == -1
	})

	for _, assignment := range testAssignments {
		providerAddr := types.NewProviderConsAddress(assignment.ProviderAddr)
		pk.SetValidatorConsumerPubKey(ctx, assignment.ConsumerId, providerAddr, *assignment.ConsumerKey)
	}

	result := pk.GetAllValidatorConsumerPubKeys(ctx, &consumerID)
	require.Equal(t, expectedGetAllOneConsumerOrder, result)

	result = pk.GetAllValidatorConsumerPubKeys(ctx, nil)
	require.Len(t, result, len(testAssignments))
}

func TestValidatorByConsumerAddrCRUD(t *testing.T) {
	consumerID := CONSUMER_ID
	providerAddr := types.NewProviderConsAddress([]byte("providerAddr"))
	consumerAddr := types.NewConsumerConsAddress([]byte("consumerAddr"))

	keeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	keeper.SetValidatorByConsumerAddr(ctx, consumerID, consumerAddr, providerAddr)

	providerAddrResult, found := keeper.GetValidatorByConsumerAddr(ctx, consumerID, consumerAddr)
	require.True(t, found, "provider address not found")
	require.NotEmpty(t, providerAddrResult, "provider address is empty")
	require.Equal(t, providerAddr, providerAddrResult)

	keeper.DeleteValidatorByConsumerAddr(ctx, consumerID, consumerAddr)
	providerAddrResult, found = keeper.GetValidatorByConsumerAddr(ctx, consumerID, consumerAddr)
	require.False(t, found, "provider address was found")
	require.Empty(t, providerAddrResult, "provider address not empty")
	require.NotEqual(t, providerAddr, providerAddrResult)
}

func TestGetAllValidatorsByConsumerAddr(t *testing.T) {
	pk, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	seed := time.Now().UnixNano()
	rng := rand.New(rand.NewSource(seed))

	consumerIDs := []uint64{1, 2, 3}
	numAssignments := 10
	testAssignments := []types.ValidatorByConsumerAddr{}
	for i := range numAssignments {
		consumerAddr := cryptotestutil.NewCryptoIdentityFromIntSeed(i).ConsumerConsAddress()
		providerAddr := cryptotestutil.NewCryptoIdentityFromIntSeed(numAssignments + i).ProviderConsAddress()
		testAssignments = append(testAssignments,
			types.ValidatorByConsumerAddr{
				ConsumerId:   consumerIDs[rng.Intn(len(consumerIDs))],
				ConsumerAddr: consumerAddr.ToSdkConsAddr(),
				ProviderAddr: providerAddr.ToSdkConsAddr(),
			},
		)
	}
	// select a consumerId with more than two assignments
	var consumerID uint64
	for i := range consumerIDs {
		consumerID = consumerIDs[i]
		count := 0
		for _, assignment := range testAssignments {
			if assignment.ConsumerId == consumerID {
				count++
			}
		}
		if count > 2 {
			break
		}
	}
	expectedGetAllOneConsumerOrder := []types.ValidatorByConsumerAddr{}
	for _, assignment := range testAssignments {
		if assignment.ConsumerId == consumerID {
			expectedGetAllOneConsumerOrder = append(expectedGetAllOneConsumerOrder, assignment)
		}
	}
	// sorting by ValidatorByConsumerAddr.ConsumerAddr
	sort.Slice(expectedGetAllOneConsumerOrder, func(i, j int) bool {
		return bytes.Compare(expectedGetAllOneConsumerOrder[i].ConsumerAddr, expectedGetAllOneConsumerOrder[j].ConsumerAddr) == -1
	})

	for _, assignment := range testAssignments {
		consumerAddr := types.NewConsumerConsAddress(assignment.ConsumerAddr)
		providerAddr := types.NewProviderConsAddress(assignment.ProviderAddr)
		pk.SetValidatorByConsumerAddr(ctx, assignment.ConsumerId, consumerAddr, providerAddr)
	}

	result := pk.GetAllValidatorsByConsumerAddr(ctx, &consumerID)
	require.Equal(t, expectedGetAllOneConsumerOrder, result)

	result = pk.GetAllValidatorsByConsumerAddr(ctx, nil)
	require.Len(t, result, len(testAssignments))
}

func TestConsumerAddrsToPruneCRUD(t *testing.T) {
	consumerID := CONSUMER_ID
	consumerAddr1 := types.NewConsumerConsAddress([]byte("consumerAddr1"))
	consumerAddr2 := types.NewConsumerConsAddress([]byte("consumerAddr2"))

	keeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	ts1 := ctx.BlockTime()
	ts2 := ts1.Add(time.Hour)

	addrsToPrune := keeper.GetConsumerAddrsToPrune(ctx, consumerID, ts1).Addresses
	require.Empty(t, addrsToPrune)

	keeper.AppendConsumerAddrsToPrune(ctx, consumerID, ts1, consumerAddr1)

	addrsToPrune = keeper.GetConsumerAddrsToPrune(ctx, consumerID, ts1).Addresses
	require.NotEmpty(t, addrsToPrune, "addresses to prune is empty")
	require.Len(t, addrsToPrune, 1, "addresses to prune is not len 1")
	require.Equal(t, addrsToPrune[0], consumerAddr1.ToSdkConsAddr().Bytes())

	keeper.AppendConsumerAddrsToPrune(ctx, consumerID, ts2, consumerAddr2)

	addrsToPrune = keeper.GetConsumerAddrsToPrune(ctx, consumerID, ts2).Addresses
	require.NotEmpty(t, addrsToPrune, "addresses to prune is empty")
	require.Len(t, addrsToPrune, 1, "addresses to prune is not len 1")
	require.Equal(t, addrsToPrune[0], consumerAddr2.ToSdkConsAddr().Bytes())

	keeper.DeleteConsumerAddrsToPrune(ctx, consumerID, ts1)
	addrsToPrune = keeper.GetConsumerAddrsToPrune(ctx, consumerID, ts1).Addresses
	require.Empty(t, addrsToPrune, "addresses to prune was returned")
	addrsToPrune = keeper.GetConsumerAddrsToPrune(ctx, consumerID, ts2).Addresses
	require.NotEmpty(t, addrsToPrune, "addresses to prune is empty")
	require.Len(t, addrsToPrune, 1, "addresses to prune is not len 1")
	require.Equal(t, addrsToPrune[0], consumerAddr2.ToSdkConsAddr().Bytes())

	keeper.AppendConsumerAddrsToPrune(ctx, consumerID, ts1, consumerAddr1)

	addrsToPrune = keeper.ConsumeConsumerAddrsToPrune(ctx, consumerID, ts1).Addresses
	require.NotEmpty(t, addrsToPrune, "addresses to prune was returned")
	require.Len(t, addrsToPrune, 1, "addresses to prune is not len 1")
	require.Equal(t, addrsToPrune[0], consumerAddr1.ToSdkConsAddr().Bytes())
	addrsToPrune = keeper.GetConsumerAddrsToPrune(ctx, consumerID, ts1).Addresses
	require.Empty(t, addrsToPrune, "addresses to prune was returned")
	addrsToPrune = keeper.GetConsumerAddrsToPrune(ctx, consumerID, ts2).Addresses
	require.NotEmpty(t, addrsToPrune, "addresses to prune is empty")
	require.Len(t, addrsToPrune, 1, "addresses to prune is not len 1")
	require.Equal(t, addrsToPrune[0], consumerAddr2.ToSdkConsAddr().Bytes())
}

func TestGetAllConsumerAddrsToPrune(t *testing.T) {
	pk, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	seed := time.Now().UnixNano()
	rng := rand.New(rand.NewSource(seed))

	consumerIDs := []uint64{1, 2, 3}
	numAssignments := 10
	testAssignments := []types.ConsumerAddrsToPrune{}
	for i := range numAssignments {
		consumerAddresses := types.AddressList{}
		for j := 0; j < 2*(i+1); j++ {
			addr := cryptotestutil.NewCryptoIdentityFromIntSeed(i * j).SDKValConsAddress()
			consumerAddresses.Addresses = append(consumerAddresses.Addresses, addr)
		}
		testAssignments = append(testAssignments,
			types.ConsumerAddrsToPrune{
				ConsumerId:    consumerIDs[rng.Intn(len(consumerIDs))],
				PruneTs:       time.Now().UTC(),
				ConsumerAddrs: &consumerAddresses,
			},
		)
	}
	// select a consumerId with more than two assignments
	var consumerID uint64
	for i := range consumerIDs {
		consumerID = consumerIDs[i]
		count := 0
		for _, assignment := range testAssignments {
			if assignment.ConsumerId == consumerID {
				count++
			}
		}
		if count > 2 {
			break
		}
	}
	expectedGetAllOrder := []types.ConsumerAddrsToPrune{}
	for _, assignment := range testAssignments {
		if assignment.ConsumerId == consumerID {
			expectedGetAllOrder = append(expectedGetAllOrder, assignment)
		}
	}
	// sorting by ConsumerAddrsToPrune.PruneTs
	sort.Slice(expectedGetAllOrder, func(i, j int) bool {
		return expectedGetAllOrder[i].PruneTs.Before(expectedGetAllOrder[j].PruneTs)
	})

	for _, assignment := range testAssignments {
		for _, addr := range assignment.ConsumerAddrs.Addresses {
			consumerAddr := types.NewConsumerConsAddress(addr)
			pk.AppendConsumerAddrsToPrune(ctx, assignment.ConsumerId, assignment.PruneTs, consumerAddr)
		}
	}

	result := pk.GetAllConsumerAddrsToPrune(ctx, consumerID)
	require.Equal(t, expectedGetAllOrder, result)
}

// checkCorrectPruningProperty checks that the pruning property is correct for a given
// consumer chain. See AppendConsumerAddrsToPrune for a formulation of the property.
func checkCorrectPruningProperty(ctx sdk.Context, k providerkeeper.Keeper, consumerID uint64) bool {
	/*
		For each consumer address cAddr in ValidatorByConsumerAddr,
		  - either there exists a provider address pAddr in ValidatorConsumerPubKey,
		    s.t. hash(ValidatorConsumerPubKey(pAddr)) = cAddr
		  - or there exists a timestamp in ConsumerAddrsToPrune s.t. cAddr in ConsumerAddrsToPrune(timestamp)
	*/
	willBePruned := map[string]bool{}
	for _, consAddrToPrune := range k.GetAllConsumerAddrsToPrune(ctx, consumerID) {
		for _, cAddr := range consAddrToPrune.ConsumerAddrs.Addresses {
			willBePruned[string(cAddr)] = true
		}
	}

	good := true
	for _, valByConsAddr := range k.GetAllValidatorsByConsumerAddr(ctx, nil) {
		if _, ok := willBePruned[string(valByConsAddr.ConsumerAddr)]; ok {
			// Address will be pruned, everything is fine.
			continue
		}

		// Try to find a validator who has this consumer address currently assigned
		isCurrentlyAssigned := false
		for _, valconsPubKey := range k.GetAllValidatorConsumerPubKeys(ctx, &valByConsAddr.ConsumerId) {
			consumerAddr, _ := vaastypes.TMCryptoPublicKeyToConsAddr(*valconsPubKey.ConsumerKey)
			if consumerAddr.Equals(sdk.ConsAddress(valByConsAddr.ConsumerAddr)) {
				isCurrentlyAssigned = true
				break
			}
		}

		if !isCurrentlyAssigned {
			// Will not be pruned, and is not currently assigned: violation
			good = false
			break
		}
	}

	return good
}

func TestAssignConsensusKeyForConsumerChain(t *testing.T) {
	consumerId := uint64(0)
	providerIdentities := []*cryptotestutil.CryptoIdentity{
		cryptotestutil.NewCryptoIdentityFromIntSeed(0),
		cryptotestutil.NewCryptoIdentityFromIntSeed(1),
	}
	consumerIdentities := []*cryptotestutil.CryptoIdentity{
		cryptotestutil.NewCryptoIdentityFromIntSeed(2),
		cryptotestutil.NewCryptoIdentityFromIntSeed(3),
	}

	testCases := []struct {
		name string
		// State-mutating mockSetup specific to this test case
		mockSetup func(sdk.Context, providerkeeper.Keeper, testkeeper.MockedKeepers)
		doActions func(sdk.Context, providerkeeper.Keeper)
	}{
		/*
			0. Consumer not in the right phase: Assign PK0->CK0 and error
			1. Consumer      launched: Assign PK0->CK0 and retrieve PK0->CK0
			2. Consumer      launched: Assign PK0->CK0, PK0->CK1 and retrieve PK0->CK1
			3. Consumer      launched: Assign PK0->CK0, PK1->CK0 and error
			4. Consumer      launched: Assign PK1->PK0 and error
			5. Consumer    registered: Assign PK0->CK0 and retrieve PK0->CK0
			6. Consumer    registered: Assign PK0->CK0, PK0->CK1 and retrieve PK0->CK1
			7. Consumer    registered: Assign PK0->CK0, PK1->CK0 and error
			8. Consumer    registered: Assign PK1->PK0 and error
		*/
		{
			name:      "0",
			mockSetup: func(ctx sdk.Context, k providerkeeper.Keeper, mocks testkeeper.MockedKeepers) {},
			doActions: func(ctx sdk.Context, k providerkeeper.Keeper) {
				err := k.AssignConsumerKey(ctx, consumerId,
					providerIdentities[0].SDKStakingValidator(),
					consumerIdentities[0].TMProtoCryptoPublicKey(),
				)
				require.Error(t, err)
				_, found := k.GetValidatorByConsumerAddr(ctx, consumerId,
					consumerIdentities[0].ConsumerConsAddress())
				require.False(t, found)
			},
		},
		{
			name: "1",
			mockSetup: func(sdkCtx sdk.Context, k providerkeeper.Keeper, mocks testkeeper.MockedKeepers) {
				gomock.InOrder(
					mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(sdkCtx,
						consumerIdentities[0].SDKValConsAddress(),
					).Return(stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound),
				)
			},
			doActions: func(ctx sdk.Context, k providerkeeper.Keeper) {
				k.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_LAUNCHED)
				err := k.AssignConsumerKey(ctx, consumerId,
					providerIdentities[0].SDKStakingValidator(),
					consumerIdentities[0].TMProtoCryptoPublicKey(),
				)
				require.NoError(t, err)
				providerAddr, found := k.GetValidatorByConsumerAddr(ctx, consumerId,
					consumerIdentities[0].ConsumerConsAddress())
				require.True(t, found)
				require.Equal(t, providerIdentities[0].ProviderConsAddress(), providerAddr)
			},
		},
		{
			name: "2",
			mockSetup: func(ctx sdk.Context, k providerkeeper.Keeper, mocks testkeeper.MockedKeepers) {
				gomock.InOrder(
					mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx,
						consumerIdentities[0].SDKValConsAddress(),
					).Return(stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound),
					mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx,
						consumerIdentities[1].SDKValConsAddress(),
					).Return(stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound),
					mocks.MockStakingKeeper.EXPECT().UnbondingTime(ctx),
				)
			},
			doActions: func(sdkCtx sdk.Context, k providerkeeper.Keeper) {
				k.SetConsumerPhase(sdkCtx, consumerId, types.CONSUMER_PHASE_LAUNCHED)
				err := k.AssignConsumerKey(sdkCtx, consumerId,
					providerIdentities[0].SDKStakingValidator(),
					consumerIdentities[0].TMProtoCryptoPublicKey(),
				)
				require.NoError(t, err)
				err = k.AssignConsumerKey(sdkCtx, consumerId,
					providerIdentities[0].SDKStakingValidator(),
					consumerIdentities[1].TMProtoCryptoPublicKey(),
				)
				require.NoError(t, err)
				providerAddr, found := k.GetValidatorByConsumerAddr(sdkCtx, consumerId,
					consumerIdentities[1].ConsumerConsAddress())
				require.True(t, found)
				require.Equal(t, providerIdentities[0].ProviderConsAddress(), providerAddr)
			},
		},
		{
			name: "3",
			mockSetup: func(ctx sdk.Context, k providerkeeper.Keeper, mocks testkeeper.MockedKeepers) {
				gomock.InOrder(
					mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx,
						consumerIdentities[0].SDKValConsAddress(),
					).Return(stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound),
					mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx,
						consumerIdentities[0].SDKValConsAddress(),
					).Return(stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound),
				)
			},
			doActions: func(ctx sdk.Context, k providerkeeper.Keeper) {
				k.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_LAUNCHED)
				err := k.AssignConsumerKey(ctx, consumerId,
					providerIdentities[0].SDKStakingValidator(),
					consumerIdentities[0].TMProtoCryptoPublicKey(),
				)
				require.NoError(t, err)
				err = k.AssignConsumerKey(ctx, consumerId,
					providerIdentities[1].SDKStakingValidator(),
					consumerIdentities[0].TMProtoCryptoPublicKey(),
				)
				require.Error(t, err)
				providerAddr, found := k.GetValidatorByConsumerAddr(ctx, consumerId,
					consumerIdentities[0].ConsumerConsAddress())
				require.True(t, found)
				require.Equal(t, providerIdentities[0].ProviderConsAddress(), providerAddr)
			},
		},
		{
			name: "4",
			mockSetup: func(ctx sdk.Context, k providerkeeper.Keeper, mocks testkeeper.MockedKeepers) {
				gomock.InOrder(
					mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx,
						providerIdentities[0].SDKValConsAddress(),
					).Return(providerIdentities[0].SDKStakingValidator(), nil),
				)
			},
			doActions: func(ctx sdk.Context, k providerkeeper.Keeper) {
				k.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_LAUNCHED)
				err := k.AssignConsumerKey(ctx, consumerId,
					providerIdentities[1].SDKStakingValidator(),
					providerIdentities[0].TMProtoCryptoPublicKey(),
				)
				require.Error(t, err)
			},
		},
		{
			name: "5",
			mockSetup: func(ctx sdk.Context, k providerkeeper.Keeper, mocks testkeeper.MockedKeepers) {
				gomock.InOrder(
					mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx,
						consumerIdentities[0].SDKValConsAddress(),
					).Return(stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound),
				)
			},
			doActions: func(ctx sdk.Context, k providerkeeper.Keeper) {
				k.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_INITIALIZED)
				err := k.AssignConsumerKey(ctx, consumerId,
					providerIdentities[0].SDKStakingValidator(),
					consumerIdentities[0].TMProtoCryptoPublicKey(),
				)
				require.NoError(t, err)
				providerAddr, found := k.GetValidatorByConsumerAddr(ctx, consumerId,
					consumerIdentities[0].ConsumerConsAddress())
				require.True(t, found)
				require.Equal(t, providerIdentities[0].ProviderConsAddress(), providerAddr)
			},
		},
		{
			name: "6",
			mockSetup: func(ctx sdk.Context, k providerkeeper.Keeper, mocks testkeeper.MockedKeepers) {
				gomock.InOrder(
					mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx,
						consumerIdentities[0].SDKValConsAddress(),
					).Return(stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound),
					mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx,
						consumerIdentities[1].SDKValConsAddress(),
					).Return(stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound),
				)
			},
			doActions: func(ctx sdk.Context, k providerkeeper.Keeper) {
				k.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_INITIALIZED)
				err := k.AssignConsumerKey(ctx, consumerId,
					providerIdentities[0].SDKStakingValidator(),
					consumerIdentities[0].TMProtoCryptoPublicKey(),
				)
				require.NoError(t, err)
				err = k.AssignConsumerKey(ctx, consumerId,
					providerIdentities[0].SDKStakingValidator(),
					consumerIdentities[1].TMProtoCryptoPublicKey(),
				)
				require.NoError(t, err)
				providerAddr, found := k.GetValidatorByConsumerAddr(ctx, consumerId,
					consumerIdentities[1].ConsumerConsAddress())
				require.True(t, found)
				require.Equal(t, providerIdentities[0].ProviderConsAddress(), providerAddr)
			},
		},
		{
			name: "7",
			mockSetup: func(ctx sdk.Context, k providerkeeper.Keeper, mocks testkeeper.MockedKeepers) {
				gomock.InOrder(
					mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx,
						consumerIdentities[0].SDKValConsAddress(),
					).Return(stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound),
					mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx,
						consumerIdentities[0].SDKValConsAddress(),
					).Return(stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound),
				)
			},
			doActions: func(ctx sdk.Context, k providerkeeper.Keeper) {
				k.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_INITIALIZED)
				err := k.AssignConsumerKey(ctx, consumerId,
					providerIdentities[0].SDKStakingValidator(),
					consumerIdentities[0].TMProtoCryptoPublicKey(),
				)
				require.NoError(t, err)
				err = k.AssignConsumerKey(ctx, consumerId,
					providerIdentities[1].SDKStakingValidator(),
					consumerIdentities[0].TMProtoCryptoPublicKey(),
				)
				require.Error(t, err)
				providerAddr, found := k.GetValidatorByConsumerAddr(ctx, consumerId,
					consumerIdentities[0].ConsumerConsAddress())
				require.True(t, found)
				require.Equal(t, providerIdentities[0].ProviderConsAddress(), providerAddr)
			},
		},
		{
			name: "8",
			mockSetup: func(ctx sdk.Context, k providerkeeper.Keeper, mocks testkeeper.MockedKeepers) {
				gomock.InOrder(
					mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx,
						providerIdentities[0].SDKValConsAddress(),
					).Return(providerIdentities[0].SDKStakingValidator(), nil),
				)
			},
			doActions: func(ctx sdk.Context, k providerkeeper.Keeper) {
				k.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_INITIALIZED)
				err := k.AssignConsumerKey(ctx, consumerId,
					providerIdentities[1].SDKStakingValidator(),
					providerIdentities[0].TMProtoCryptoPublicKey(),
				)
				require.Error(t, err)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))

			tc.mockSetup(ctx, k, mocks)
			tc.doActions(ctx, k)
			require.True(t, checkCorrectPruningProperty(ctx, k, consumerId))

			ctrl.Finish()
		})
	}
}

// TestCannotReassignDefaultKeyAssignment tests that a validator cannot assign the key it uses on a provider,
// to a consumer, if that validator has not already assigned the key to a consumer.
// Ie. the default key assignment is that a validator uses the same key on a provider as it does on a consumer.
// A validator cannot re-assign the default key assignment if it already uses the default key assignment.
//
// TODO: guarding against edge cases like this could be avoided by refactoring key assignment logic to have less cyclomatic complexity.
func TestCannotReassignDefaultKeyAssignment(t *testing.T) {
	// We only need one identity, a single validator / single key
	cId := cryptotestutil.NewCryptoIdentityFromIntSeed(49827489)

	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	providerKeeper.SetConsumerPhase(ctx, CONSUMER_ID, types.CONSUMER_PHASE_INITIALIZED)

	// Mock that the validator is validating with the single key, as confirmed by provider's staking keeper
	gomock.InOrder(
		mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx,
			cId.SDKValConsAddress(),
		).Return(cId.SDKStakingValidator(), nil), // nil == no error
	)

	// AssignConsumerKey should return an error if we try to re-assign the already existing default key assignment
	err := providerKeeper.AssignConsumerKey(ctx, CONSUMER_ID, cId.SDKStakingValidator(), cId.TMProtoCryptoPublicKey())
	require.Error(t, err)

	// Confirm we're not returning an error for some other reason
	require.Equal(t, "a validator cannot assign the default key assignment unless its key on that consumer has already been assigned: cannot re-assign default key assignment", err.Error())
}

// Represents the validator set of a chain
type ValSet struct {
	identities []*cryptotestutil.CryptoIdentity
	// indexed by same index as identities
	power []int64
}

func CreateValSet(identities []*cryptotestutil.CryptoIdentity) ValSet {
	return ValSet{
		identities: identities,
		power:      make([]int64, len(identities)),
	}
}

// Apply a list of validator power updates
func (vs *ValSet) apply(updates []abci.ValidatorUpdate) {
	// precondition: updates must all have unique keys
	// note: an insertion index should always be found
	for _, u := range updates {
		for i, id := range vs.identities { // n2 looping but n is tiny
			cons, _ := vaastypes.TMCryptoPublicKeyToConsAddr(u.PubKey)
			if id.SDKValConsAddress().Equals(cons) {
				vs.power[i] = u.Power
			}
		}
	}
}

// A key assignment action to be done
type Assignment struct {
	val stakingtypes.Validator
	ck  tmprotocrypto.PublicKey
}

// TestSimulatedAssignmentsAndUpdateApplication tests a series
// of simulated scenarios where random key assignments and validator
// set updates are generated.
func TestSimulatedAssignmentsAndUpdateApplication(t *testing.T) {
	CONSUMERID := CONSUMER_ID
	// The number of full test executions to run
	NUM_EXECUTIONS := 100
	// Each test execution mimics the adding of a consumer chain and the
	// assignments and power updates of several blocks
	NUM_BLOCKS_PER_EXECUTION := 40
	// The number of validators to be simulated
	NUM_VALIDATORS := 4
	// The number of keys that can be used. Keeping this number small is
	// good because it increases the chance that different assignments will
	// use the same keys, which is something we want to test.
	NUM_ASSIGNABLE_KEYS := 12
	// The maximum number of key assignment actions to simulate in each
	// simulated block, and before the consumer chain is registered.
	NUM_ASSIGNMENTS_PER_BLOCK_MAX := 8

	// Create some identities for the simulated provider validators to use
	providerIDS := []*cryptotestutil.CryptoIdentity{}
	// Create some identities which the provider validators can assign to the consumer chain
	assignableIDS := []*cryptotestutil.CryptoIdentity{}
	for i := range NUM_VALIDATORS {
		providerIDS = append(providerIDS, cryptotestutil.NewCryptoIdentityFromIntSeed(i))
	}
	// Notice that the assignable identities include the provider identities
	for i := 0; i < NUM_VALIDATORS+NUM_ASSIGNABLE_KEYS; i++ {
		assignableIDS = append(assignableIDS, cryptotestutil.NewCryptoIdentityFromIntSeed(i))
	}

	seed := time.Now().UnixNano()
	rng := rand.New(rand.NewSource(seed))

	// Helper: simulates creation of staking module EndBlock updates.
	getStakingUpdates := func() (ret []abci.ValidatorUpdate) {
		// Get a random set of validators to update. It is important to test subsets of all validators.
		validators := rng.Perm(len(providerIDS))[0:rng.Intn(len(providerIDS)+1)]
		for _, i := range validators {
			// Power 0, 1, or 2 represents
			// deletion, update (from 0 or 2), update (from 0 or 1)
			power := rng.Intn(3)
			ret = append(ret, abci.ValidatorUpdate{
				PubKey: providerIDS[i].TMProtoCryptoPublicKey(),
				Power:  int64(power),
			})
		}
		return
	}

	// Helper: simulates creation of assignment tx's to be done.
	getAssignments := func() (ret []Assignment) {
		for i, numAssignments := 0, rng.Intn(NUM_ASSIGNMENTS_PER_BLOCK_MAX); i < numAssignments; i++ {
			randomIxP := rng.Intn(len(providerIDS))
			randomIxC := rng.Intn(len(assignableIDS))
			ret = append(ret, Assignment{
				val: providerIDS[randomIxP].SDKStakingValidator(),
				ck:  assignableIDS[randomIxC].TMProtoCryptoPublicKey(),
			})
		}
		return
	}

	// Run a randomly simulated execution and test that desired properties hold
	// Helper: run a randomly simulated scenario where a consumer chain is added
	// (after key assignment actions are done), followed by a series of validator power updates
	// and key assignments tx's. For each simulated 'block', the validator set replication
	// properties and the pruning property are checked.
	runRandomExecution := func() {
		k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))

		// Create validator sets for the provider and consumer. These are used to check the validator set
		// replication property.
		providerValset := CreateValSet(providerIDS)
		// NOTE: consumer must have space for provider identities because default key assignments are to provider keys
		consumerValset := CreateValSet(assignableIDS)

		// Sanity check that the validator set update is initialised to 0, for clarity.
		require.Equal(t, k.GetValidatorSetUpdateId(ctx), uint64(0))

		// Mock calls to GetLastValidatorPower to return directly from the providerValset
		mocks.MockStakingKeeper.EXPECT().GetLastValidatorPower(
			gomock.Any(),
			gomock.Any(),
		).DoAndReturn(func(_ any, valAddr sdk.ValAddress) (int64, error) {
			// When the mocked method is called, locate the appropriate validator
			// in the provider valset and return its power.
			for i, id := range providerIDS {
				if id.SDKStakingValidator().GetOperator() == valAddr.String() {
					return providerValset.power[i], nil
				}
			}
			panic("must find validator")
			// This can be called 0 or more times per block depending on the random
			// assignments that occur
		}).AnyTimes()

		// This implements the assumption that all the provider IDS are added
		// to the system at the beginning of the simulation.
		mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(
			gomock.Any(),
			gomock.Any(),
		).DoAndReturn(func(_ any, consP sdk.ConsAddress) (stakingtypes.Validator, bool) {
			for _, id := range providerIDS {
				if id.SDKValConsAddress().Equals(consP) {
					return id.SDKStakingValidator(), true
				}
			}
			return stakingtypes.Validator{}, false
		}).AnyTimes()

		// Helper: apply some updates to both the provider and consumer valsets
		// and increment the provider vscid.
		applyUpdatesAndIncrementVSCID := func(updates []abci.ValidatorUpdate) {
			providerValset.apply(updates)

			var bondedValidators []stakingtypes.Validator
			for _, v := range providerValset.identities {
				pkAny, _ := codectypes.NewAnyWithValue(v.ConsensusSDKPubKey())

				bondedValidators = append(bondedValidators, stakingtypes.Validator{
					OperatorAddress: v.SDKValOpAddress().String(),
					ConsensusPubkey: pkAny,
				})
			}

			nextValidators, err := k.CreateConsumerValidators(ctx, CONSUMERID, bondedValidators)
			require.NoError(t, err)
			valSet, err := k.GetConsumerValSet(ctx, CONSUMERID)
			require.NoError(t, err)
			updates = providerkeeper.DiffValidators(valSet, nextValidators)
			err = k.SetConsumerValSet(ctx, CONSUMERID, nextValidators)
			require.NoError(t, err)

			consumerValset.apply(updates)
			// Simulate the VSCID update in EndBlock
			k.IncrementValidatorSetUpdateId(ctx)
		}

		// Helper: apply some key assignment transactions to the system
		applyAssignments := func(assignments []Assignment) {
			for _, a := range assignments {
				// ignore err return, it can be possible for an error to occur
				_ = k.AssignConsumerKey(ctx, CONSUMERID, a.val, a.ck)
			}
		}

		// Set the unbonding time to 60s so that a key is prunable after 60s
		unbondingTimeInNs := 60 * time.Second // 60 seconds
		mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(unbondingTimeInNs, nil).AnyTimes()

		// The consumer chain has not yet been registered
		// Apply some randomly generated key assignments
		assignments := getAssignments()

		applyAssignments(assignments)
		// And generate a random provider valset which, in the real system, will
		// be put into the consumer genesis.
		stakingUpdates := getStakingUpdates()

		applyUpdatesAndIncrementVSCID(stakingUpdates)

		// Register the consumer chain
		k.SetConsumerClientId(ctx, CONSUMER_ID, "")

		// Set the greatest block time up to which keys have been pruned. At the beginning, no pruning has taken
		// place, so we set `greatestPrunedBlockTime` to 0, and set the current block time to 1.
		greatestPrunedBlockTime := int64(0)
		ctx = ctx.WithBlockTime(time.Unix(0, 1))

		// Simulate a number of 'blocks'
		// Each block consists of a number of random key assignment tx's
		// and a random set of validator power updates
		for range NUM_BLOCKS_PER_EXECUTION {
			stakingUpdates = getStakingUpdates()
			assignments = getAssignments()

			// Generate and apply assignments and power updates
			applyAssignments(assignments)
			applyUpdatesAndIncrementVSCID(stakingUpdates)

			// prune all keys that can be pruned up to the current block time
			greatestPrunedBlockTime = ctx.BlockTime().UnixNano()
			k.PruneKeyAssignments(ctx, CONSUMER_ID)

			// Increase the block time by a small random amount up to UnbondingTime / 10. We do not increase the block time
			// by UnbondingTime so that in the upcoming iteration of this `for` loop (i.e., new block), not all the keys
			// previously (in this current block) set to be prunable are pruned.
			ctx = ctx.WithBlockTime(time.Unix(0, ctx.BlockTime().UnixNano()+rng.Int63n(unbondingTimeInNs.Nanoseconds())/10))

			/*

				Property: Validator Set Replication
				Each validator set on the provider must be replicated on the consumer.
				The property in the real system is somewhat weaker, because the consumer chain can
				forward updates to tendermint in batches.
				(See https://github.com/cosmos/ibc/blob/main/spec/app/ics-028-cross-chain-validation/system_model_and_properties.md#system-properties)
				We test the stronger property, because we abstract over implementation of the consumer
				chain. The stronger property implies the weaker property.

			*/

			// Check validator set replication forward direction
			for i, idP := range providerValset.identities {
				// For each active validator on the provider chain
				if 0 < providerValset.power[i] {
					// Get the assigned key
					ck, found := k.GetValidatorConsumerPubKey(ctx, CONSUMER_ID, idP.ProviderConsAddress())
					if !found {
						// Use default if unassigned
						ck = idP.TMProtoCryptoPublicKey()
					}
					consC, err := vaastypes.TMCryptoPublicKeyToConsAddr(ck)
					require.NoError(t, err)
					// Find the corresponding consumer validator (must always be found)
					for j, idC := range consumerValset.identities {
						if consC.Equals(idC.SDKValConsAddress()) {
							// Ensure powers are the same
							require.Equal(t, providerValset.power[i], consumerValset.power[j])
						}
					}
				}
			}
			// Check validator set replication backward direction
			for i := range consumerValset.identities {
				// For each active validator on the consumer chain
				consC := consumerValset.identities[i].ConsumerConsAddress()
				if 0 < consumerValset.power[i] {
					// Get the provider who assigned the key
					consP := k.GetProviderAddrFromConsumerAddr(ctx, CONSUMER_ID, consC)
					// Find the corresponding provider validator (must always be found)
					for j, idP := range providerValset.identities {
						if idP.SDKValConsAddress().Equals(consP.ToSdkConsAddr()) {
							// Ensure powers are the same
							require.Equal(t, providerValset.power[j], consumerValset.power[i])
						}
					}
				}
			}

			/*
				Property: Pruning (bounded storage)
				Check that all keys have been or will eventually be pruned.
			*/
			require.True(t, checkCorrectPruningProperty(ctx, k, CONSUMER_ID))

			/*
				Property: Correct Consumer Initiated Slash Lookup

				Check that since the last pruning took place, it has never been possible to have
				two different provider addresses for the same consumer address.
				We know that the queried provider address was correct at least once,
				from checking the validator set replication property. These two facts
				together guarantee that the slash lookup is always correct.
			*/

			// For each validator on the consumer, record the corresponding provider
			// address as looked up on the provider using `GetProviderAddrFromConsumerAddr`
			// at a given block time.
			// consumer consAddr -> block time -> provider consAddr
			consumerAddrToBlockTimeToProviderAddr := map[string]map[uint64]string{}

			// Build up the consumerAddrToBlockTimeToProviderAddr data structure
			for i := range consumerValset.identities {
				// For each active validator on the consumer chain
				consC := consumerValset.identities[i].ConsumerConsAddress()
				if 0 < consumerValset.power[i] {
					// Get the provider who assigned the key
					consP := k.GetProviderAddrFromConsumerAddr(ctx, CONSUMER_ID, consC)

					if _, found := consumerAddrToBlockTimeToProviderAddr[consC.String()]; !found {
						consumerAddrToBlockTimeToProviderAddr[consC.String()] = map[uint64]string{}
					}

					consumerAddrToBlockTimeToProviderAddr[consC.String()][uint64(ctx.BlockTime().UnixNano())] = consP.String()
				}
			}

			// Check that, for each consumer address known at some block with blockTime st. greatestPrunedBlockTime < blockTime,
			// there were never two providers with this consumer address.
			for _, blockTimeToProviderAddr := range consumerAddrToBlockTimeToProviderAddr {
				seen := map[string]bool{}
				for blockTime, consP := range blockTimeToProviderAddr {
					if uint64(greatestPrunedBlockTime) < blockTime {
						seen[consP] = true
					}
				}
				// Having len(seen) >= 2 implies that we had at least 2 different provider addresses that at some point
				// had the exact same consumer address since the last pruning took place. This should not be possible!
				require.True(t, len(seen) < 2)
			}

		}
		ctrl.Finish()
	}

	for range NUM_EXECUTIONS {
		runRandomExecution()
	}
}

// TestValidatorConsensusKeyInUseSeesPausedConsumers verifies that the
// collision guard covering validator creation also looks at consumers in the
// PAUSED phase.
//
// The guard exists to stop a new provider validator from taking a consensus
// key that some other validator already runs as its assigned key on a
// consumer: on that consumer both would resolve to the same consensus address,
// and a validator set carrying one address twice is not something a chain can
// apply. A pause does not release those assignments -- they are exactly what
// the resume snapshot is rebuilt from -- so a paused consumer's keys have to
// stay visible here. Missing them lets the colliding validator be created
// during the pause and delivers the duplicate at resume.
func TestValidatorConsensusKeyInUseSeesPausedConsumers(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	assigned := cryptotestutil.NewCryptoIdentityFromIntSeed(4820)
	incumbent := cryptotestutil.NewCryptoIdentityFromIntSeed(4821)

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_PAUSED)
	k.SetValidatorByConsumerAddr(ctx, consumerId,
		assigned.ConsumerConsAddress(), incumbent.ProviderConsAddress())

	// A brand-new provider validator whose own consensus key is the one already
	// serving as the incumbent's assigned key on the paused consumer.
	newVal := assigned.SDKStakingValidator()
	valAddr, err := sdk.ValAddressFromBech32(newVal.GetOperator())
	require.NoError(t, err)
	mocks.MockStakingKeeper.EXPECT().GetValidator(gomock.Any(), valAddr).Return(newVal, nil).AnyTimes()

	require.True(t, k.ValidatorConsensusKeyInUse(ctx, valAddr),
		"a consensus key assigned on a PAUSED consumer must still count as in use")
}

// TestAssignConsumerKeyOnPausedConsumer verifies that key assignment is
// available while a consumer is paused.
//
// A pause resolves either into LAUNCHED again or into STOPPED, and it lasts up
// to MaxPauseDuration. Rejecting assignment throughout would leave a validator
// that needs to change its consumer key -- a compromised key being the case
// that matters -- waiting for someone else's governance decision before it can
// act.
func TestAssignConsumerKeyOnPausedConsumer(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	provider := cryptotestutil.NewCryptoIdentityFromIntSeed(4830)
	consumerKey := cryptotestutil.NewCryptoIdentityFromIntSeed(4831)

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_PAUSED)

	mocks.MockStakingKeeper.EXPECT().
		GetValidatorByConsAddr(gomock.Any(), consumerKey.SDKValConsAddress()).
		Return(stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound)

	require.NoError(t, k.AssignConsumerKey(ctx, consumerId,
		provider.SDKStakingValidator(), consumerKey.TMProtoCryptoPublicKey()))

	got, found := k.GetValidatorByConsumerAddr(ctx, consumerId, consumerKey.ConsumerConsAddress())
	require.True(t, found, "the assignment must be recorded on a paused consumer")
	require.Equal(t, provider.ProviderConsAddress(), got)
}

// TestAssignConsumerKeyOnPausedConsumerPrunesRatherThanDeletes verifies that
// replacing a key on a paused consumer schedules the old consumer address for
// pruning instead of deleting the mapping outright.
//
// The delete-immediately branch is for consumers that never launched, where no
// evidence can name the old address. A paused consumer has launched and is the
// one case guaranteed to have downtime state in flight -- a pause is entered by
// a successful downtime challenge -- and both the challenge lookup and the
// re-submission defence resolve an accused consumer address through this
// mapping. Dropping it at assignment time would strand that state.
func TestAssignConsumerKeyOnPausedConsumerPrunesRatherThanDeletes(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	provider := cryptotestutil.NewCryptoIdentityFromIntSeed(4840)
	oldKey := cryptotestutil.NewCryptoIdentityFromIntSeed(4841)
	newKey := cryptotestutil.NewCryptoIdentityFromIntSeed(4842)

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_PAUSED)

	unbonding := 3 * 7 * 24 * time.Hour
	mocks.MockStakingKeeper.EXPECT().
		GetValidatorByConsAddr(gomock.Any(), gomock.Any()).
		Return(stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(unbonding, nil)

	require.NoError(t, k.AssignConsumerKey(ctx, consumerId,
		provider.SDKStakingValidator(), oldKey.TMProtoCryptoPublicKey()))
	require.NoError(t, k.AssignConsumerKey(ctx, consumerId,
		provider.SDKStakingValidator(), newKey.TMProtoCryptoPublicKey()))

	_, found := k.GetValidatorByConsumerAddr(ctx, consumerId, oldKey.ConsumerConsAddress())
	require.True(t, found,
		"the replaced consumer address must stay resolvable until pruning, not be deleted")

	pruneTime := ctx.BlockTime().Add(unbonding)
	oldConsumerAddr := oldKey.ConsumerConsAddress()
	require.Contains(t, k.GetConsumerAddrsToPrune(ctx, consumerId, pruneTime).Addresses,
		oldConsumerAddr.ToSdkConsAddr().Bytes(),
		"the replaced consumer address must be queued for pruning after the unbonding period")
}
