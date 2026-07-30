package keeper_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	tmtypes "github.com/cometbft/cometbft/types"

	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"
	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	"github.com/cosmos/cosmos-sdk/codec/address"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	cryptotestutil "github.com/allinbits/vaas/testutil/crypto"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

const (
	rotationTestChainID  = "consumer-chain-rotation"
	rotationTestFeeDenom = "uphoton"
)

var (
	errRotationBondedSet = errors.New("cannot read the bonded set")
	errRotationSend      = errors.New("cannot hand the packet to IBC")
)

// setupRotationConsumer wires one launched consumer with the registered chain
// id, client, minimum evidence height and initialization parameters the
// downtime pipeline reads, and returns its id and client id. Client ids are
// derived from the consumer id because ConsumerClients indexes them uniquely.
func setupRotationConsumer(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context, spawnTime time.Time) (uint64, string) {
	t.Helper()

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	clientId := fmt.Sprintf("07-tendermint-%d", consumerId)
	k.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerChainId(ctx, consumerId, rotationTestChainID)
	k.SetConsumerClientId(ctx, consumerId, clientId)
	k.SetEquivocationEvidenceMinHeight(ctx, consumerId, 1)
	require.NoError(t, k.SetConsumerInitializationParameters(ctx, consumerId, types.ConsumerInitializationParameters{
		SpawnTime: spawnTime,
	}))

	return consumerId, clientId
}

// allowWithheldFeePayout wires what PayWithheldFees needs on a successful
// challenge -- resolving the record's provider consensus address to its
// validator's account and moving the escrowed coins out of the consumer's fee
// pool -- and returns the outputs it produced, so a test can assert the
// escrow was actually paid rather than just cleared.
func allowWithheldFeePayout(mocks testkeeper.MockedKeepers, payee stakingtypes.Validator) *[]banktypes.Output {
	paid := &[]banktypes.Output{}
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(gomock.Any(), gomock.Any()).Return(payee, nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(address.NewBech32Codec("cosmosvaloper")).AnyTimes()
	mocks.MockBankKeeper.EXPECT().GetBalance(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(sdk.NewCoin(rotationTestFeeDenom, math.NewInt(10_000))).AnyTimes()
	mocks.MockBankKeeper.EXPECT().InputOutputCoins(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ banktypes.Input, outputs []banktypes.Output) error {
			*paid = append(*paid, outputs...)
			return nil
		}).AnyTimes()

	return paid
}

// allowRotationSnapshotCompute wires the mocks a rotation snapshot needs to be
// computed: the bonded set it is built from, post-rotation, so
// bondedAfterRotation carries the validator's new consensus key -- which is what
// x/staking already holds by the time it calls the hook.
func allowRotationSnapshotCompute(mocks testkeeper.MockedKeepers, bondedAfterRotation []stakingtypes.Validator) {
	mocks.MockStakingKeeper.EXPECT().MaxValidators(gomock.Any()).Return(uint32(100), nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().GetBondedValidatorsByPower(gomock.Any()).Return(bondedAfterRotation, nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().GetLastValidatorPower(gomock.Any(), gomock.Any()).Return(int64(1000), nil).AnyTimes()
}

// stakingValidatorFor builds a bonded staking validator holding consPubKey,
// operated by operator. Two of these sharing an operator model a validator
// before and after a consensus key rotation.
func stakingValidatorFor(t *testing.T, operator sdk.ValAddress, consPubKey cryptotypes.PubKey) stakingtypes.Validator {
	t.Helper()

	val, err := stakingtypes.NewValidator(operator.String(), consPubKey, stakingtypes.NewDescription("", "", "", "", ""))
	require.NoError(t, err)
	val.Status = stakingtypes.Bonded
	val.Tokens = sdk.DefaultPowerReduction
	val.DelegatorShares = math.LegacyNewDecFromInt(sdk.DefaultPowerReduction)

	return val
}

// challengeDowntime builds and submits a genuine downtime challenge for
// claimedHeight: a commit carrying signer's precommit at that height, sealed
// into the header one above it by LastCommitHash. The light-client verification
// of that header is overridden by the caller.
func challengeDowntime(
	t *testing.T,
	k providerkeeper.Keeper,
	ctx sdk.Context,
	consumerId uint64,
	signer tmtypes.PrivValidator,
	claimedHeight int64,
) error {
	t.Helper()

	pubKey, err := signer.GetPubKey()
	require.NoError(t, err)

	blockID := cryptotestutil.MakeBlockID([]byte("blockhash"), 1, []byte("partshash"))
	vote, err := tmtypes.MakeVote(signer, rotationTestChainID, 0, claimedHeight, 0, tmproto.PrecommitType, blockID, ctx.BlockTime())
	require.NoError(t, err)
	commit := &tmtypes.Commit{
		Height:     claimedHeight,
		Round:      0,
		BlockID:    blockID,
		Signatures: []tmtypes.CommitSig{vote.CommitSig()},
	}

	return k.HandleChallengeConsumerDowntime(ctx, &types.MsgChallengeConsumerDowntime{
		Signer:        "cosmos1qypqxpq9qcrsszgse4wwrq4vt3s2r0y8ryqhx7",
		ConsumerId:    consumerId,
		ValidatorAddr: pubKey.Address(),
		ClaimedHeight: claimedHeight,
		Header: &ibctmtypes.Header{
			SignedHeader: &tmproto.SignedHeader{
				Header: &tmproto.Header{
					ChainID:        rotationTestChainID,
					Height:         claimedHeight + 1,
					LastCommitHash: commit.Hash(),
				},
				Commit: &tmproto.Commit{},
			},
		},
		LastCommit:      commit.ToProto(),
		ValidatorPubkey: pubKey.Bytes(),
	})
}

// doubleVoteBy builds a self-contained double-sign by signer on the rotation
// test consumer: two precommits for the same height and round on different
// blocks, both signed by signer, so the evidence verifies against signer's
// public key alone.
func doubleVoteBy(t *testing.T, signer tmtypes.PrivValidator, height int64, at time.Time) *tmtypes.DuplicateVoteEvidence {
	t.Helper()

	pubKey, err := signer.GetPubKey()
	require.NoError(t, err)
	valSet := tmtypes.NewValidatorSet([]*tmtypes.Validator{tmtypes.NewValidator(pubKey, 1)})

	return &tmtypes.DuplicateVoteEvidence{
		VoteA: cryptotestutil.MakeAndSignVote(
			cryptotestutil.MakeBlockID([]byte("blockhash1"), 1000, []byte("partshash")),
			height, at, valSet, signer, rotationTestChainID,
		),
		VoteB: cryptotestutil.MakeAndSignVote(
			cryptotestutil.MakeBlockID([]byte("blockhash2"), 1000, []byte("partshash")),
			height, at, valSet, signer, rotationTestChainID,
		),
		TotalVotingPower: 1,
		ValidatorPower:   1,
		Timestamp:        at,
	}
}

// rotationSigningInfo models the ValidatorSigningInfo store x/slashing keeps,
// keyed by consensus address, and what a rotation does to it: the entry is
// written at the new consensus address and the one at the old address is
// deleted, so nothing answers there afterwards. Every x/slashing call the
// equivocation punishment path makes reads that store, and JailUntil and
// Tombstone fail outright when the entry they name is missing.
type rotationSigningInfo struct {
	infos map[string]*slashingtypes.ValidatorSigningInfo
}

func newRotationSigningInfo(addr sdk.ConsAddress) *rotationSigningInfo {
	return &rotationSigningInfo{infos: map[string]*slashingtypes.ValidatorSigningInfo{
		addr.String(): {Address: addr.String()},
	}}
}

// rotate mirrors x/slashing's performConsensusPubKeyUpdate, which its
// AfterConsensusPubKeyUpdate hook runs on every rotation.
func (s *rotationSigningInfo) rotate(oldAddr, newAddr sdk.ConsAddress) {
	info := s.infos[oldAddr.String()]
	info.Address = newAddr.String()
	s.infos[newAddr.String()] = info
	delete(s.infos, oldAddr.String())
}

func (s *rotationSigningInfo) at(addr sdk.ConsAddress) *slashingtypes.ValidatorSigningInfo {
	return s.infos[addr.String()]
}

// wire answers the mocked slashing keeper out of the store the way x/slashing
// answers out of its own: a missing entry reads as not tombstoned, and makes
// both JailUntil and Tombstone error.
func (s *rotationSigningInfo) wire(mocks testkeeper.MockedKeepers) {
	mocks.MockSlashingKeeper.EXPECT().IsTombstoned(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, addr sdk.ConsAddress) bool {
			info, found := s.infos[addr.String()]
			return found && info.Tombstoned
		}).AnyTimes()
	mocks.MockSlashingKeeper.EXPECT().JailUntil(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, addr sdk.ConsAddress, jailedUntil time.Time) error {
			info, found := s.infos[addr.String()]
			if !found {
				return slashingtypes.ErrNoSigningInfoFound.Wrap("cannot jail validator that does not have any signing information")
			}
			info.JailedUntil = jailedUntil
			return nil
		}).AnyTimes()
	mocks.MockSlashingKeeper.EXPECT().Tombstone(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, addr sdk.ConsAddress) error {
			info, found := s.infos[addr.String()]
			if !found {
				return slashingtypes.ErrNoSigningInfoFound.Wrap("cannot tombstone validator that does not have any signing information")
			}
			if info.Tombstoned {
				return slashingtypes.ErrValidatorTombstoned.Wrap("cannot tombstone validator that is already tombstoned")
			}
			info.Tombstoned = true
			return nil
		}).AnyTimes()
}

// TestConsPubKeyRotationKeepsAssignedValidatorDowntimeStateResolvable covers the
// downtime and fee state a rotation must carry over for a validator that has an
// assigned consumer key: the consumer keeps validating under that key, so the
// consumer's accusations keep resolving through the reverse mapping -- which the
// rotation repoints at the new provider address -- while the state they are
// judged against is keyed by the address they resolved to before it.
//
// Concretely, once the same window resolves to a different address:
//
//   - the accepted-window record and the pruned acceptance floor no longer
//     recognise the window, so the same infraction is accepted a second time and
//     the validator is slashed twice for one offence;
//   - the queued slash is no longer found by a challenge, which looks for it
//     under the address it resolves the accused to, so a false accusation
//     becomes unfalsifiable and executes;
//   - the consumer validator set entry no longer covers the accused, so genuine
//     evidence is rejected as naming a validator outside the set;
//   - the epoch downtime mark is no longer seen by the next fee distribution,
//     which reads it under the validator's live consensus address, so the
//     validator is paid for an epoch it had accepted downtime evidence in and
//     nothing is escrowed for a challenge to repay.
func TestConsPubKeyRotationKeepsAssignedValidatorDowntimeStateResolvable(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	ctx = ctx.WithBlockTime(windowEndTime)
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)
	k.SetInfractionParams(ctx, infractionParams)
	k.OverrideWindowEndTimestampForTest(func(sdk.Context, string, int64) (time.Time, error) {
		return windowEndTime, nil
	})
	k.OverrideVerifyDowntimeChallengeHeaderForTest(func(sdk.Context, string, *ibctmtypes.Header) error {
		return nil
	})
	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), gomock.Any()).Return(ibcexported.Active).AnyTimes()

	cid, _ := setupRotationConsumer(t, k, ctx, windowEndTime.Add(-30*24*time.Hour))

	// The validator's assigned consumer key is what the consumer validates
	// under, and what a challenge has to produce a sealed signature for, so it
	// is the key held by the signer here.
	consumerSigner := tmtypes.NewMockPV()
	consumerPubKey, err := consumerSigner.GetPubKey()
	require.NoError(t, err)
	consumerSDKPubKey, err := cryptocodec.FromCmtPubKeyInterface(consumerPubKey)
	require.NoError(t, err)
	assignedKeyHolder := stakingValidatorFor(t, sdk.ValAddress(consumerSDKPubKey.Address()), consumerSDKPubKey)
	assignedKey, err := assignedKeyHolder.CmtConsPublicKey()
	require.NoError(t, err)
	consumerAddr := types.NewConsumerConsAddress(sdk.ConsAddress(consumerPubKey.Address()))

	oldIdentity := cryptotestutil.NewCryptoIdentityFromIntSeed(41)
	newIdentity := cryptotestutil.NewCryptoIdentityFromIntSeed(42)
	oldProviderAddr := oldIdentity.ProviderConsAddress()
	newProviderAddr := newIdentity.ProviderConsAddress()

	k.SetValidatorConsumerPubKey(ctx, cid, oldProviderAddr, assignedKey)
	k.SetValidatorByConsumerAddr(ctx, cid, consumerAddr, oldProviderAddr)
	require.NoError(t, k.SetConsumerValidator(ctx, cid, types.ConsensusValidator{
		ProviderConsAddr: oldProviderAddr.ToSdkConsAddr(),
		Power:            1000,
		PublicKey:        &assignedKey,
		JoinHeight:       7,
	}))

	// P is resolved from a recorded epoch share so pricing needs no staking.
	k.SetEpochShareRecord(ctx, cid, windowEndTime, math.NewInt(1000))
	mocks.MockPhotonKeeper.EXPECT().ConversionRate(gomock.Any()).Return(math.LegacyNewDec(2), nil).AnyTimes()

	// A window this pair had accepted long enough ago that its record was
	// pruned and replaced by the acceptance floor.
	require.NoError(t, k.DowntimeWindowFloors.Set(ctx, collections.Join(cid, oldProviderAddr.ToSdkConsAddr().Bytes()), int64(50)))

	// Accept evidence for window [93, 100].
	evidence := vaastypes.NewEvidencePacketData(consumerAddr.ToSdkConsAddr(), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, k.HandleConsumerEvidencePacket(ctx, cid, evidence))

	// The infraction epoch's reward exclusion is marked here rather than left
	// to evidence acceptance: acceptance only marks it when the infraction's
	// epoch has not yet paid out, and the recorded epoch share this test uses
	// to price the slash means it has. What the rotation must carry over is the
	// mark itself, whichever way it was set.
	k.MarkEpochDowntime(ctx, cid, oldProviderAddr.ToSdkConsAddr())

	oldWindowKey := collections.Join3(cid, oldProviderAddr.ToSdkConsAddr().Bytes(), int64(100))
	_, err = k.PendingDowntimeSlashes.Get(ctx, oldWindowKey)
	require.NoError(t, err)
	require.True(t, k.IsEpochDowntime(ctx, cid, oldProviderAddr.ToSdkConsAddr()))

	// A fee share escrowed by an earlier exclusion, which a successful
	// challenge has to be able to pay back.
	require.NoError(t, k.WithheldFeeRecords.Set(ctx, collections.Join(cid, oldProviderAddr.ToSdkConsAddr().Bytes()),
		types.WithheldFeeRecord{
			ConsumerId:       cid,
			ProviderConsAddr: oldProviderAddr.ToSdkConsAddr().Bytes(),
			Amount:           sdk.NewCoin(rotationTestFeeDenom, math.NewInt(500)),
			ExpiresAt:        windowEndTime.Add(7 * 24 * time.Hour),
		}))
	rotatedOperator := cryptotestutil.NewCryptoIdentityFromIntSeed(43).SDKValOpAddress()
	paid := allowWithheldFeePayout(mocks, stakingValidatorFor(t, rotatedOperator, newIdentity.ConsensusSDKPubKey()))

	// --- the validator rotates its provider consensus key ---
	// The consumer's view of it does not change (it keeps the assigned key), so
	// no snapshot is queued and no send mocks are needed.
	require.NoError(t, k.Hooks().AfterConsensusPubKeyUpdate(
		ctx, oldIdentity.ConsensusSDKPubKey(), newIdentity.ConsensusSDKPubKey(), sdk.Coin{},
	))
	require.Empty(t, k.GetPendingVSCPackets(ctx, cid),
		"a consumer whose view of the validator is unchanged must not be snapshotted")

	newAddrBz := newProviderAddr.ToSdkConsAddr().Bytes()
	newWindowKey := collections.Join3(cid, newAddrBz, int64(100))

	// Every piece of state the consumer's accusations are judged against now
	// stands under the address they resolve to.
	require.Equal(t, newProviderAddr, k.GetProviderAddrFromConsumerAddr(ctx, cid, consumerAddr))

	pending, err := k.PendingDowntimeSlashes.Get(ctx, newWindowKey)
	require.NoError(t, err, "the queued slash must follow the rotation")
	require.Equal(t, int64(93), pending.WindowStartHeight)
	require.Equal(t, newAddrBz, pending.ProviderConsAddr,
		"the queued slash must name the address it is keyed under")
	_, err = k.PendingDowntimeSlashes.Get(ctx, oldWindowKey)
	require.ErrorIs(t, err, collections.ErrNotFound)

	accepted, err := k.AcceptedDowntimeWindows.Get(ctx, newWindowKey)
	require.NoError(t, err, "the accepted window must follow the rotation")
	require.Equal(t, int64(93), accepted.WindowStart)
	_, err = k.AcceptedDowntimeWindows.Get(ctx, oldWindowKey)
	require.ErrorIs(t, err, collections.ErrNotFound)

	floor, err := k.DowntimeWindowFloors.Get(ctx, collections.Join(cid, newAddrBz))
	require.NoError(t, err, "the acceptance floor must follow the rotation")
	require.Equal(t, int64(50), floor)
	_, err = k.DowntimeWindowFloors.Get(ctx, collections.Join(cid, oldProviderAddr.ToSdkConsAddr().Bytes()))
	require.ErrorIs(t, err, collections.ErrNotFound)

	consumerVal, found := k.GetConsumerValidator(ctx, cid, newProviderAddr)
	require.True(t, found, "the consumer validator set entry must follow the rotation")
	require.Equal(t, newAddrBz, consumerVal.ProviderConsAddr)
	require.Equal(t, int64(7), consumerVal.JoinHeight, "the join height must survive the move")
	require.False(t, k.IsConsumerValidator(ctx, cid, oldProviderAddr))

	require.True(t, k.IsEpochDowntime(ctx, cid, newProviderAddr.ToSdkConsAddr()),
		"the epoch downtime mark must follow the rotation, since fee distribution reads it under the live consensus address")
	require.False(t, k.IsEpochDowntime(ctx, cid, oldProviderAddr.ToSdkConsAddr()))

	withheld, err := k.WithheldFeeRecords.Get(ctx, collections.Join(cid, newAddrBz))
	require.NoError(t, err, "the withheld fee record must follow the rotation")
	require.Equal(t, newAddrBz, withheld.ProviderConsAddr)
	require.Equal(t, math.NewInt(500), withheld.Amount.Amount)
	_, err = k.WithheldFeeRecords.Get(ctx, collections.Join(cid, oldProviderAddr.ToSdkConsAddr().Bytes()))
	require.ErrorIs(t, err, collections.ErrNotFound)

	// (1) the same window cannot be accepted a second time under the new
	// address: one offence, one slash.
	duplicate := vaastypes.NewEvidencePacketData(consumerAddr.ToSdkConsAddr(), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	err = k.HandleConsumerEvidencePacket(ctx, cid, duplicate)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already accepted")

	// (2) neither can a window the pruned acceptance floor stands for.
	belowFloor := vaastypes.NewEvidencePacketData(consumerAddr.ToSdkConsAddr(), 43, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	err = k.HandleConsumerEvidencePacket(ctx, cid, belowFloor)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pruned acceptance floor")

	// (3) the queued slash is still falsifiable: the challenge resolves the
	// accused to the new address and finds it there.
	require.NoError(t, challengeDowntime(t, k, ctx, cid, consumerSigner, 95))
	require.Equal(t, types.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))
	_, err = k.PendingDowntimeSlashes.Get(ctx, newWindowKey)
	require.ErrorIs(t, err, collections.ErrNotFound)

	// (4) and the escrow it caused is paid back to the rotated validator, not
	// left behind at an address the record no longer names.
	require.Len(t, *paid, 1, "the withheld fee record must be paid on a successful challenge")
	require.Equal(t, sdk.AccAddress(rotatedOperator).String(), (*paid)[0].Address)
	require.Equal(t, sdk.NewCoins(sdk.NewCoin(rotationTestFeeDenom, math.NewInt(500))), (*paid)[0].Coins)
	_, err = k.WithheldFeeRecords.Get(ctx, collections.Join(cid, newAddrBz))
	require.ErrorIs(t, err, collections.ErrNotFound)
}

// TestConsPubKeyRotationLeavesDefaultKeyValidatorAcceptanceStatePut is the other
// half of the rule: a validator with no assigned consumer key validates the
// consumer with its provider key, so the rotation changes the identity the
// consumer already validated under. Evidence and challenges about that identity
// name the pre-rotation consumer address and, with no reverse mapping to resolve
// through, keep resolving to the pre-rotation provider address -- so the
// acceptance bookkeeping they are judged against has to stay there. Moving it
// would leave a re-submitted window unrecognised and the queued slash
// unchallengeable, the very failures the assigned-key case moves state to avoid.
//
// The fee-exclusion state moves regardless, since fee distribution reads it
// under the validator's live consensus address.
func TestConsPubKeyRotationLeavesDefaultKeyValidatorAcceptanceStatePut(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	ctx = ctx.WithBlockTime(windowEndTime)
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)
	k.SetInfractionParams(ctx, infractionParams)
	k.OverrideWindowEndTimestampForTest(func(sdk.Context, string, int64) (time.Time, error) {
		return windowEndTime, nil
	})
	k.OverrideVerifyDowntimeChallengeHeaderForTest(func(sdk.Context, string, *ibctmtypes.Header) error {
		return nil
	})
	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), gomock.Any()).Return(ibcexported.Active).AnyTimes()

	cid, _ := setupRotationConsumer(t, k, ctx, windowEndTime.Add(-30*24*time.Hour))

	// The validator runs its provider consensus key on the consumer, so that
	// key is what signs consumer blocks and what a challenge exhibits.
	oldSigner := tmtypes.NewMockPV()
	oldCmtPubKey, err := oldSigner.GetPubKey()
	require.NoError(t, err)
	oldSDKPubKey, err := cryptocodec.FromCmtPubKeyInterface(oldCmtPubKey)
	require.NoError(t, err)
	oldProviderAddr := types.NewProviderConsAddress(sdk.ConsAddress(oldCmtPubKey.Address()))

	newIdentity := cryptotestutil.NewCryptoIdentityFromIntSeed(51)
	newProviderAddr := newIdentity.ProviderConsAddress()

	// One operator, two consensus keys: what x/staking holds before and after
	// applying the rotation. The hook fires once the new one is already stored.
	operator := sdk.ValAddress(oldSDKPubKey.Address())
	valBefore := stakingValidatorFor(t, operator, oldSDKPubKey)
	valAfter := stakingValidatorFor(t, operator, newIdentity.ConsensusSDKPubKey())
	allowRotationSnapshotCompute(mocks, []stakingtypes.Validator{valAfter})
	mocks.MockChannelV2Keeper.EXPECT().SendPacket(gomock.Any(), gomock.Any()).
		Return(&channeltypesv2.MsgSendPacketResponse{Sequence: 1}, nil).Times(1)

	beforeKey, err := valBefore.CmtConsPublicKey()
	require.NoError(t, err)
	require.NoError(t, k.SetConsumerValidator(ctx, cid, types.ConsensusValidator{
		ProviderConsAddr: oldProviderAddr.ToSdkConsAddr(),
		Power:            1000,
		PublicKey:        &beforeKey,
		JoinHeight:       1,
	}))

	k.SetEpochShareRecord(ctx, cid, windowEndTime, math.NewInt(1000))
	mocks.MockPhotonKeeper.EXPECT().ConversionRate(gomock.Any()).Return(math.LegacyNewDec(2), nil).AnyTimes()

	oldAddrBz := oldProviderAddr.ToSdkConsAddr().Bytes()
	evidence := vaastypes.NewEvidencePacketData(oldProviderAddr.ToSdkConsAddr(), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, k.HandleConsumerEvidencePacket(ctx, cid, evidence))

	// The infraction epoch's reward exclusion is marked here rather than left
	// to evidence acceptance: acceptance only marks it when the infraction's
	// epoch has not yet paid out, and the recorded epoch share this test uses
	// to price the slash means it has. What the rotation must carry over is the
	// mark itself, whichever way it was set.
	k.MarkEpochDowntime(ctx, cid, oldAddrBz)

	require.NoError(t, k.WithheldFeeRecords.Set(ctx, collections.Join(cid, oldAddrBz), types.WithheldFeeRecord{
		ConsumerId:       cid,
		ProviderConsAddr: oldAddrBz,
		Amount:           sdk.NewCoin(rotationTestFeeDenom, math.NewInt(500)),
		ExpiresAt:        windowEndTime.Add(7 * 24 * time.Hour),
	}))
	allowWithheldFeePayout(mocks, valAfter)

	require.NoError(t, k.Hooks().AfterConsensusPubKeyUpdate(
		ctx, oldSDKPubKey, newIdentity.ConsensusSDKPubKey(), sdk.Coin{},
	))

	oldWindowKey := collections.Join3(cid, oldAddrBz, int64(100))
	newAddrBz := newProviderAddr.ToSdkConsAddr().Bytes()

	// The acceptance bookkeeping stays where the pre-rotation identity's
	// evidence resolves.
	_, err = k.PendingDowntimeSlashes.Get(ctx, oldWindowKey)
	require.NoError(t, err)
	_, err = k.PendingDowntimeSlashes.Get(ctx, collections.Join3(cid, newAddrBz, int64(100)))
	require.ErrorIs(t, err, collections.ErrNotFound)
	_, err = k.AcceptedDowntimeWindows.Get(ctx, oldWindowKey)
	require.NoError(t, err)
	_, err = k.AcceptedDowntimeWindows.Get(ctx, collections.Join3(cid, newAddrBz, int64(100)))
	require.ErrorIs(t, err, collections.ErrNotFound)

	// The fee-exclusion state moves: fee distribution reads it under the
	// validator's live consensus address, which is the rotated one.
	require.True(t, k.IsEpochDowntime(ctx, cid, newProviderAddr.ToSdkConsAddr()))
	require.False(t, k.IsEpochDowntime(ctx, cid, oldProviderAddr.ToSdkConsAddr()))
	withheld, err := k.WithheldFeeRecords.Get(ctx, collections.Join(cid, newAddrBz))
	require.NoError(t, err)
	require.Equal(t, newAddrBz, withheld.ProviderConsAddr)
	_, err = k.WithheldFeeRecords.Get(ctx, collections.Join(cid, oldAddrBz))
	require.ErrorIs(t, err, collections.ErrNotFound)

	// The consumer was handed the rotated key at once, and its stored set is
	// rebuilt under the new address by that snapshot.
	require.True(t, k.IsConsumerValidator(ctx, cid, newProviderAddr))
	require.False(t, k.IsConsumerValidator(ctx, cid, oldProviderAddr))

	// The queued slash is still falsifiable, under the identity the consumer
	// accused: the challenge exhibits the old consensus key's sealed signature.
	require.NoError(t, challengeDowntime(t, k, ctx, cid, oldSigner, 95))
	require.Equal(t, types.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))
	_, err = k.PendingDowntimeSlashes.Get(ctx, oldWindowKey)
	require.ErrorIs(t, err, collections.ErrNotFound)
}

// TestConsPubKeyRotationKeepsPreRotationEvidenceAcceptable pins that a rotation
// cannot shed accusations that have not been delivered yet.
//
// A validator with no assigned consumer key validates the consumer with its
// provider key, so the rotation moves its entry in the consumer's stored
// validator set to the rotated address -- the snapshot rebuilds that set from
// the bonded validators, and so does the next epoch boundary. An accusation
// about a window that ended before the rotation names the pre-rotation identity
// and, with no key assignment to resolve through, resolves to the pre-rotation
// address, where the set no longer holds the validator. Looked up there alone it
// counts as naming a validator outside the consumer's set and is dropped, and
// with it every accusation not yet accepted when the rotation landed: the
// packets in flight, the ones the consumer has queued but not sent, and the
// window the consumer had not closed yet -- up to DowntimeEvidenceMaxAge worth
// of offences, shed for the price of a rotation.
//
// So the accused is looked up again under the address x/staking says it holds
// now, and the accusation is judged on its merits: priced and queued under the
// identity it named, which is where a challenge looks for it and where the
// re-submission defence stands, while the epoch downtime mark goes to the live
// address fee distribution reads it back under.
func TestConsPubKeyRotationKeepsPreRotationEvidenceAcceptable(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	ctx = ctx.WithBlockTime(windowEndTime)
	k.SetInfractionParams(ctx, downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour))
	k.OverrideWindowEndTimestampForTest(func(sdk.Context, string, int64) (time.Time, error) {
		return windowEndTime, nil
	})
	k.OverrideVerifyDowntimeChallengeHeaderForTest(func(sdk.Context, string, *ibctmtypes.Header) error {
		return nil
	})
	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), gomock.Any()).Return(ibcexported.Active).AnyTimes()

	cid, _ := setupRotationConsumer(t, k, ctx, windowEndTime.Add(-30*24*time.Hour))

	// The validator runs its provider consensus key on the consumer, so that
	// key is the identity the consumer accuses and the one a challenge exhibits.
	oldSigner := tmtypes.NewMockPV()
	oldCmtPubKey, err := oldSigner.GetPubKey()
	require.NoError(t, err)
	oldSDKPubKey, err := cryptocodec.FromCmtPubKeyInterface(oldCmtPubKey)
	require.NoError(t, err)
	oldProviderAddr := types.NewProviderConsAddress(sdk.ConsAddress(oldCmtPubKey.Address()))
	oldAddrBz := oldProviderAddr.ToSdkConsAddr().Bytes()

	newIdentity := cryptotestutil.NewCryptoIdentityFromIntSeed(81)
	newProviderAddr := newIdentity.ProviderConsAddress()
	newAddrBz := newProviderAddr.ToSdkConsAddr().Bytes()

	operator := sdk.ValAddress(oldSDKPubKey.Address())
	valBefore := stakingValidatorFor(t, operator, oldSDKPubKey)
	valAfter := stakingValidatorFor(t, operator, newIdentity.ConsensusSDKPubKey())
	allowRotationSnapshotCompute(mocks, []stakingtypes.Validator{valAfter})
	mocks.MockChannelV2Keeper.EXPECT().SendPacket(gomock.Any(), gomock.Any()).
		Return(&channeltypesv2.MsgSendPacketResponse{Sequence: 1}, nil).Times(1)

	// x/staking answers for both of the validator's addresses once the rotation
	// is recorded: the new one directly, the rotated-away one through the
	// old-to-new consensus address mapping.
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, consAddr sdk.ConsAddress) (stakingtypes.Validator, error) {
			if consAddr.Equals(oldProviderAddr.ToSdkConsAddr()) || consAddr.Equals(newProviderAddr.ToSdkConsAddr()) {
				return valAfter, nil
			}
			return stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound
		}).AnyTimes()

	beforeKey, err := valBefore.CmtConsPublicKey()
	require.NoError(t, err)
	require.NoError(t, k.SetConsumerValidator(ctx, cid, types.ConsensusValidator{
		ProviderConsAddr: oldProviderAddr.ToSdkConsAddr(),
		Power:            1000,
		PublicKey:        &beforeKey,
		JoinHeight:       1,
	}))

	// --- the validator rotates its provider consensus key ---
	require.NoError(t, k.Hooks().AfterConsensusPubKeyUpdate(
		ctx, oldSDKPubKey, newIdentity.ConsensusSDKPubKey(), sdk.Coin{},
	))
	require.True(t, k.IsConsumerValidator(ctx, cid, newProviderAddr))
	require.False(t, k.IsConsumerValidator(ctx, cid, oldProviderAddr),
		"the stored set holds the validator under the address it runs now, which is what makes the lookup below a rotation lookup")

	// A window that ended before the rotation, reported now: the consumer had
	// not closed it, or the packet had not been delivered, when the rotation
	// landed. P=1000uphoton resolved from the epoch record covering it, C=2.
	k.SetEpochShareRecord(ctx, cid, windowEndTime, math.NewInt(1000))
	mocks.MockPhotonKeeper.EXPECT().ConversionRate(gomock.Any()).Return(math.LegacyNewDec(2), nil).AnyTimes()

	evidence := vaastypes.NewEvidencePacketData(oldProviderAddr.ToSdkConsAddr(), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, k.HandleConsumerEvidencePacket(ctx, cid, evidence),
		"a rotation must not make a pre-rotation window's evidence unacceptable")

	// The slash is queued under the identity the consumer accused, priced at
	// receipt time: M = 6/8 = 0.75, slashTokens = P*M/C = 1000*0.75/2 = 375.
	oldWindowKey := collections.Join3(cid, oldAddrBz, int64(100))
	pending, err := k.PendingDowntimeSlashes.Get(ctx, oldWindowKey)
	require.NoError(t, err)
	require.Equal(t, oldAddrBz, pending.ProviderConsAddr)
	require.Equal(t, int64(93), pending.WindowStartHeight)
	require.True(t, math.NewInt(375).Equal(pending.SlashTokens), "expected 375, got %s", pending.SlashTokens)
	_, err = k.PendingDowntimeSlashes.Get(ctx, collections.Join3(cid, newAddrBz, int64(100)))
	require.ErrorIs(t, err, collections.ErrNotFound)

	// So is the acceptance record the re-submission defence reads.
	accepted, err := k.AcceptedDowntimeWindows.Get(ctx, oldWindowKey)
	require.NoError(t, err)
	require.Equal(t, int64(93), accepted.WindowStart)
	_, err = k.AcceptedDowntimeWindows.Get(ctx, collections.Join3(cid, newAddrBz, int64(100)))
	require.ErrorIs(t, err, collections.ErrNotFound)

	// The epoch reward exclusion this accusation carries is asserted in
	// TestPreRotationDowntimeMarksEpochExclusionUnderTheLiveAddress, which sets
	// the infraction epoch up as not yet distributed -- the only case where the
	// exclusion applies. Here the recorded epoch share that prices the slash is
	// itself proof the epoch has paid out, so whether the exclusion lands is
	// that gate's business, not this test's.

	// The window cannot be accepted a second time: one offence, one slash.
	duplicate := vaastypes.NewEvidencePacketData(oldProviderAddr.ToSdkConsAddr(), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	err = k.HandleConsumerEvidencePacket(ctx, cid, duplicate)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already accepted")

	// And the queued slash is falsifiable, under the identity the consumer
	// accused: the challenge exhibits the pre-rotation key's sealed signature.
	require.NoError(t, challengeDowntime(t, k, ctx, cid, oldSigner, 95))
	require.Equal(t, types.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))
	_, err = k.PendingDowntimeSlashes.Get(ctx, oldWindowKey)
	require.ErrorIs(t, err, collections.ErrNotFound)
}

// TestPreRotationEvidenceStillRequiresConsumerSetMembership is the bound on the
// lookup above: resolving a rotated-away address to the validator that holds it
// now decides which address the consumer's set is searched under, never whether
// the accused is in that set. A validator x/staking resolves but that the
// consumer's set does not hold -- one that never validated it, or has since
// left it -- is still rejected.
// TestPreRotationDowntimeMarksEpochExclusionUnderTheLiveAddress verifies that
// the epoch reward exclusion an accepted downtime accusation carries is marked
// under the address the validator runs now, not the rotated-away one the
// accusation names.
//
// Fee distribution reads the mark back under the address the bonded set holds
// the validator under. Marked under the accused (pre-rotation) address it is
// invisible there, so a rotation would buy the validator its full epoch share
// for an epoch it was absent for. The window here falls in the current,
// not-yet-distributed epoch -- the only case where the exclusion applies at
// all, since an epoch that has already paid out is clawed back through the
// slash instead.
func TestPreRotationDowntimeMarksEpochExclusionUnderTheLiveAddress(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	ctx = ctx.WithBlockTime(windowEndTime)
	k.SetInfractionParams(ctx, downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour))
	k.OverrideWindowEndTimestampForTest(func(sdk.Context, string, int64) (time.Time, error) {
		return windowEndTime, nil
	})

	cid, _ := setupRotationConsumer(t, k, ctx, windowEndTime.Add(-30*24*time.Hour))

	oldSigner := tmtypes.NewMockPV()
	oldCmtPubKey, err := oldSigner.GetPubKey()
	require.NoError(t, err)
	oldSDKPubKey, err := cryptocodec.FromCmtPubKeyInterface(oldCmtPubKey)
	require.NoError(t, err)
	oldProviderAddr := types.NewProviderConsAddress(sdk.ConsAddress(oldCmtPubKey.Address()))

	newIdentity := cryptotestutil.NewCryptoIdentityFromIntSeed(82)
	newProviderAddr := newIdentity.ProviderConsAddress()

	operator := sdk.ValAddress(oldSDKPubKey.Address())
	valBefore := stakingValidatorFor(t, operator, oldSDKPubKey)
	valAfter := stakingValidatorFor(t, operator, newIdentity.ConsensusSDKPubKey())
	allowRotationSnapshotCompute(mocks, []stakingtypes.Validator{valAfter})
	mocks.MockChannelV2Keeper.EXPECT().SendPacket(gomock.Any(), gomock.Any()).
		Return(&channeltypesv2.MsgSendPacketResponse{Sequence: 1}, nil).Times(1)
	mocks.MockPhotonKeeper.EXPECT().ConversionRate(gomock.Any()).Return(math.LegacyNewDec(2), nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, consAddr sdk.ConsAddress) (stakingtypes.Validator, error) {
			if consAddr.Equals(oldProviderAddr.ToSdkConsAddr()) || consAddr.Equals(newProviderAddr.ToSdkConsAddr()) {
				return valAfter, nil
			}
			return stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound
		}).AnyTimes()

	beforeKey, err := valBefore.CmtConsPublicKey()
	require.NoError(t, err)
	require.NoError(t, k.SetConsumerValidator(ctx, cid, types.ConsensusValidator{
		ProviderConsAddr: oldProviderAddr.ToSdkConsAddr(),
		Power:            1000,
		PublicKey:        &beforeKey,
		JoinHeight:       1,
	}))

	require.NoError(t, k.Hooks().AfterConsensusPubKeyUpdate(
		ctx, oldSDKPubKey, newIdentity.ConsensusSDKPubKey(), sdk.Coin{},
	))

	// No epoch share record: the window falls in the epoch still being served,
	// so the share is priced live and the exclusion is in force.
	_, distributed := k.ResolveEpochShare(ctx, cid, windowEndTime)
	require.False(t, distributed, "fixture must leave the infraction epoch undistributed")

	evidence := vaastypes.NewEvidencePacketData(oldProviderAddr.ToSdkConsAddr(), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, k.HandleConsumerEvidencePacket(ctx, cid, evidence))

	pending, err := k.PendingDowntimeSlashes.Get(ctx, collections.Join3(cid, oldProviderAddr.ToSdkConsAddr().Bytes(), int64(100)))
	require.NoError(t, err)
	require.True(t, pending.SlashTokens.IsPositive(), "a live-priced slash must be positive")

	require.True(t, k.IsEpochDowntime(ctx, cid, newProviderAddr.ToSdkConsAddr()),
		"the exclusion must be marked under the address the bonded set holds the validator under")
	require.False(t, k.IsEpochDowntime(ctx, cid, oldProviderAddr.ToSdkConsAddr()),
		"marked under the accused pre-rotation address the exclusion would be invisible to fee distribution")
}

func TestPreRotationEvidenceStillRequiresConsumerSetMembership(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	ctx = ctx.WithBlockTime(windowEndTime)
	k.SetInfractionParams(ctx, downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour))
	k.OverrideWindowEndTimestampForTest(func(sdk.Context, string, int64) (time.Time, error) {
		return windowEndTime, nil
	})

	cid, _ := setupRotationConsumer(t, k, ctx, windowEndTime.Add(-30*24*time.Hour))

	oldIdentity := cryptotestutil.NewCryptoIdentityFromIntSeed(82)
	newIdentity := cryptotestutil.NewCryptoIdentityFromIntSeed(83)
	outsider := stakingValidatorFor(t, oldIdentity.SDKValOpAddress(), newIdentity.ConsensusSDKPubKey())

	// The accused rotated, so both of its addresses resolve to it in x/staking,
	// but the consumer's set holds someone else entirely.
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(gomock.Any(), gomock.Any()).
		Return(outsider, nil).AnyTimes()

	memberIdentity := cryptotestutil.NewCryptoIdentityFromIntSeed(84)
	memberKey := memberIdentity.TMProtoCryptoPublicKey()
	require.NoError(t, k.SetConsumerValidator(ctx, cid, types.ConsensusValidator{
		ProviderConsAddr: memberIdentity.SDKValConsAddress(),
		Power:            1000,
		PublicKey:        &memberKey,
		JoinHeight:       1,
	}))

	evidence := vaastypes.NewEvidencePacketData(
		oldIdentity.SDKValConsAddress(), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"),
	)
	err := k.HandleConsumerEvidencePacket(ctx, cid, evidence)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in the validator set")
}

// TestPreRotationEquivocationIsPunishedUnderTheLiveAddress covers the identity
// the equivocation punishment path names x/slashing.
//
// A validator with no assigned consumer key signs the consumer with its provider
// consensus key, so a double-sign it commits carries that key's address and,
// with no reverse mapping to resolve through, the accusation resolves to the
// provider consensus address it ran at the time. After a rotation x/staking still
// answers for that address, through the old-to-new consensus address mapping the
// rotation records, so the slash lands -- but x/slashing does not: the rotation
// moved the validator's signing info to the new address and deleted the entry at
// the old one.
//
// Named the pre-rotation address, IsTombstoned reads no signing info and reports
// a tombstoned validator as punishable again, and JailUntil then fails on the
// missing entry, failing the whole submission after the slash has already moved
// stake. Nothing about the evidence changes between submissions, so it fails
// again every time it is submitted: a rotated validator's equivocation could not
// be punished at all, and the submitter only lost gas.
func TestPreRotationEquivocationIsPunishedUnderTheLiveAddress(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	blockTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	ctx = ctx.WithBlockTime(blockTime)
	infractionParams := types.DefaultInfractionParameters()
	infractionParams.DoubleSign.JailDuration = 30 * 24 * time.Hour
	k.SetInfractionParams(ctx, infractionParams)

	cid, _ := setupRotationConsumer(t, k, ctx, blockTime.Add(-30*24*time.Hour))

	oldSigner := tmtypes.NewMockPV()
	oldCmtPubKey, err := oldSigner.GetPubKey()
	require.NoError(t, err)
	oldSDKPubKey, err := cryptocodec.FromCmtPubKeyInterface(oldCmtPubKey)
	require.NoError(t, err)
	oldAddr := sdk.ConsAddress(oldCmtPubKey.Address())

	newIdentity := cryptotestutil.NewCryptoIdentityFromIntSeed(91)
	newAddr := newIdentity.SDKValConsAddress()
	valAfter := stakingValidatorFor(t, sdk.ValAddress(oldSDKPubKey.Address()), newIdentity.ConsensusSDKPubKey())

	// --- the validator rotates its provider consensus key ---
	signing := newRotationSigningInfo(oldAddr)
	signing.rotate(oldAddr, newAddr)
	signing.wire(mocks)
	require.Nil(t, signing.at(oldAddr),
		"the rotation leaves nothing at the pre-rotation address, which is what makes naming it fatal")

	// x/staking answers for both of the validator's addresses: the new one
	// directly, the rotated-away one through the old-to-new mapping.
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, consAddr sdk.ConsAddress) (stakingtypes.Validator, error) {
			if consAddr.Equals(oldAddr) || consAddr.Equals(newAddr) {
				return valAfter, nil
			}
			return stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound
		}).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().GetUnbondingDelegationsFromValidator(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().GetRedelegationsFromSrcValidator(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().GetLastValidatorPower(gomock.Any(), gomock.Any()).Return(int64(1000), nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().PowerReduction(gomock.Any()).Return(math.NewInt(1)).AnyTimes()

	var slashed []sdk.ConsAddress
	mocks.MockStakingKeeper.EXPECT().SlashWithInfractionReason(
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
	).DoAndReturn(func(
		_ context.Context, consAddr sdk.ConsAddress, _, power int64,
		fraction math.LegacyDec, infraction stakingtypes.Infraction,
	) (math.Int, error) {
		require.Equal(t, stakingtypes.Infraction_INFRACTION_DOUBLE_SIGN, infraction)
		slashed = append(slashed, consAddr)
		return math.LegacyNewDec(power).Mul(fraction).TruncateInt(), nil
	}).AnyTimes()

	var jailed []sdk.ConsAddress
	mocks.MockStakingKeeper.EXPECT().Jail(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, consAddr sdk.ConsAddress) error {
			jailed = append(jailed, consAddr)
			return nil
		}).AnyTimes()

	// The double-sign is committed with the key the consumer validated, which
	// the validator has since rotated away from.
	evidence := doubleVoteBy(t, oldSigner, 55, blockTime)
	require.NoError(t, k.HandleConsumerDoubleVoting(ctx, cid, evidence, oldSDKPubKey),
		"a rotation must not make an equivocation unpunishable")

	// All of the punishment landed, under the address x/staking and x/slashing
	// hold the validator under now.
	require.Equal(t, []sdk.ConsAddress{newAddr}, slashed)
	require.Equal(t, []sdk.ConsAddress{newAddr}, jailed)
	require.Equal(t, blockTime.Add(30*24*time.Hour), signing.at(newAddr).JailedUntil)
	require.True(t, signing.at(newAddr).Tombstoned)

	// And the tombstone is read back where it was written, so re-submitting the
	// same evidence is the no-op it has always been rather than a second slash.
	require.NoError(t, k.HandleConsumerDoubleVoting(ctx, cid, evidence, oldSDKPubKey))
	require.Equal(t, []sdk.ConsAddress{newAddr}, slashed, "one equivocation, one slash")
	require.Equal(t, []sdk.ConsAddress{newAddr}, jailed)
}

// TestEquivocationPunishmentStillRequiresAResolvableValidator is the bound on
// the resolution above: it reads the validator x/staking has already returned,
// so it can only ever name an address x/staking resolves. An accused x/staking
// knows no validator for is rejected before x/slashing is named at all -- the
// mocked slashing and slashing-adjacent calls are left unwired, so any of them
// fails the test.
func TestEquivocationPunishmentStillRequiresAResolvableValidator(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	blockTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	ctx = ctx.WithBlockTime(blockTime)
	k.SetInfractionParams(ctx, types.DefaultInfractionParameters())

	cid, _ := setupRotationConsumer(t, k, ctx, blockTime.Add(-30*24*time.Hour))

	signer := tmtypes.NewMockPV()
	cmtPubKey, err := signer.GetPubKey()
	require.NoError(t, err)
	sdkPubKey, err := cryptocodec.FromCmtPubKeyInterface(cmtPubKey)
	require.NoError(t, err)

	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(gomock.Any(), gomock.Any()).
		Return(stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound).AnyTimes()

	err = k.HandleConsumerDoubleVoting(ctx, cid, doubleVoteBy(t, signer, 55, blockTime), sdkPubKey)
	require.ErrorIs(t, err, slashingtypes.ErrNoValidatorForAddress)
}

// TestConsPubKeyRotationSnapshotsOnlyConsumersWhoseViewChanges covers which
// consumers are handed a rotated provider consensus key immediately. A
// validator with no assigned consumer key validates a consumer with its
// provider key, so that consumer has to learn the rotation now rather than up
// to BlocksPerEpoch blocks later, or the validator accumulates misses against
// an identity one side of the pair no longer uses. Where the validator has an
// assigned consumer key the consumer's set does not change at all, so it is not
// sent a packet.
func TestConsPubKeyRotationSnapshotsOnlyConsumersWhoseViewChanges(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	ctx = ctx.WithBlockTime(time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC))
	k.SetInfractionParams(ctx, types.DefaultInfractionParameters())

	spawnTime := ctx.BlockTime().Add(-30 * 24 * time.Hour)
	assignedCid, _ := setupRotationConsumer(t, k, ctx, spawnTime)
	defaultKeyCid, defaultKeyClientId := setupRotationConsumer(t, k, ctx, spawnTime)

	// A consumer the validator does not validate yet (still initializing) has
	// no client to send over.
	pendingCid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, pendingCid, types.CONSUMER_PHASE_INITIALIZED)

	oldIdentity := cryptotestutil.NewCryptoIdentityFromIntSeed(61)
	newIdentity := cryptotestutil.NewCryptoIdentityFromIntSeed(62)
	assignedKey := cryptotestutil.NewCryptoIdentityFromIntSeed(63)

	k.SetValidatorConsumerPubKey(ctx, assignedCid, oldIdentity.ProviderConsAddress(), assignedKey.TMProtoCryptoPublicKey())
	k.SetValidatorByConsumerAddr(ctx, assignedCid, assignedKey.ConsumerConsAddress(), oldIdentity.ProviderConsAddress())

	operator := oldIdentity.SDKValOpAddress()
	valAfter := stakingValidatorFor(t, operator, newIdentity.ConsensusSDKPubKey())
	allowRotationSnapshotCompute(mocks, []stakingtypes.Validator{valAfter})

	var sentClients []string
	var sentPackets []vaastypes.ValidatorSetChangePacketData
	mocks.MockChannelV2Keeper.EXPECT().SendPacket(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ sdk.Context, msg *channeltypesv2.MsgSendPacket) (*channeltypesv2.MsgSendPacketResponse, error) {
			sentClients = append(sentClients, msg.SourceClient)
			require.Len(t, msg.Payloads, 1)
			var packet vaastypes.ValidatorSetChangePacketData
			require.NoError(t, vaastypes.ModuleCdc.UnmarshalJSON(msg.Payloads[0].Value, &packet))
			sentPackets = append(sentPackets, packet)
			return &channeltypesv2.MsgSendPacketResponse{Sequence: 1}, nil
		}).Times(1)

	require.NoError(t, k.Hooks().AfterConsensusPubKeyUpdate(
		ctx, oldIdentity.ConsensusSDKPubKey(), newIdentity.ConsensusSDKPubKey(), sdk.Coin{},
	))

	// Exactly one packet went out, for the consumer that was validating the
	// rotated key directly, and nothing is left queued anywhere.
	require.Equal(t, []string{defaultKeyClientId}, sentClients,
		"only the consumer validating the rotated key directly may be snapshotted")
	require.Empty(t, k.GetPendingVSCPackets(ctx, defaultKeyCid))
	require.Empty(t, k.GetPendingVSCPackets(ctx, assignedCid))
	require.Empty(t, k.GetPendingVSCPackets(ctx, pendingCid))

	// The packet is a full snapshot carrying the rotated key, so the consumer
	// converges on it whatever else is in flight.
	require.Len(t, sentPackets, 1)
	require.True(t, sentPackets[0].IsSnapshot)
	rotatedCmtKey, err := valAfter.CmtConsPublicKey()
	require.NoError(t, err)
	var carriesRotatedKey bool
	for _, update := range sentPackets[0].ValidatorUpdates {
		if update.PubKey.Equal(rotatedCmtKey) {
			carriesRotatedKey = true
		}
	}
	require.True(t, carriesRotatedKey, "the snapshot must hand the consumer the rotated consensus key")

	// The consumer's stored set now names the rotated address.
	require.True(t, k.IsConsumerValidator(ctx, defaultKeyCid, newIdentity.ProviderConsAddress()))
}

// TestConsPubKeyRotationSnapshotFailuresDoNotHalt pins the hook's error
// containment for the snapshot step. x/staking calls the hook from
// ApplyAndReturnValidatorSetUpdates in EndBlock, so an error returned from here
// propagates out of EndBlock and halts the provider chain -- and any bonded
// validator can trigger the hook by rotating. Neither a snapshot that cannot be
// computed nor one that cannot be handed to IBC may do more than log.
func TestConsPubKeyRotationSnapshotFailuresDoNotHalt(t *testing.T) {
	oldIdentity := cryptotestutil.NewCryptoIdentityFromIntSeed(71)
	newIdentity := cryptotestutil.NewCryptoIdentityFromIntSeed(72)

	testCases := []struct {
		name          string
		mocks         func(mocks testkeeper.MockedKeepers)
		expectQueued  bool
		expectedNotes string
	}{
		{
			name: "the snapshot cannot be computed",
			mocks: func(mocks testkeeper.MockedKeepers) {
				mocks.MockStakingKeeper.EXPECT().MaxValidators(gomock.Any()).Return(uint32(0), errRotationBondedSet).AnyTimes()
			},
			expectQueued:  false,
			expectedNotes: "nothing is queued when the set cannot be computed",
		},
		{
			name: "the snapshot cannot be sent",
			mocks: func(mocks testkeeper.MockedKeepers) {
				mocks.MockStakingKeeper.EXPECT().MaxValidators(gomock.Any()).Return(uint32(100), nil).AnyTimes()
				mocks.MockStakingKeeper.EXPECT().GetBondedValidatorsByPower(gomock.Any()).Return([]stakingtypes.Validator{}, nil).AnyTimes()
				mocks.MockChannelV2Keeper.EXPECT().SendPacket(gomock.Any(), gomock.Any()).
					Return(nil, errRotationSend).AnyTimes()
			},
			expectQueued:  true,
			expectedNotes: "an unsent snapshot stays queued for the next epoch",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
			defer ctrl.Finish()

			k.SetInfractionParams(ctx, types.DefaultInfractionParameters())
			cid, _ := setupRotationConsumer(t, k, ctx, ctx.BlockTime())
			tc.mocks(mocks)

			require.NoError(t, k.Hooks().AfterConsensusPubKeyUpdate(
				ctx, oldIdentity.ConsensusSDKPubKey(), newIdentity.ConsensusSDKPubKey(), sdk.Coin{},
			), "the hook runs in EndBlock: returning an error would halt the provider chain")

			if tc.expectQueued {
				require.Len(t, k.GetPendingVSCPackets(ctx, cid), 1, tc.expectedNotes)
			} else {
				require.Empty(t, k.GetPendingVSCPackets(ctx, cid), tc.expectedNotes)
			}
		})
	}
}
