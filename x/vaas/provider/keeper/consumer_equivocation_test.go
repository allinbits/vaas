package keeper_test

import (
	"fmt"
	"testing"
	"time"

	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cosmossdk.io/math"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	tmtypes "github.com/cometbft/cometbft/types"

	cryptotestutil "github.com/allinbits/vaas/testutil/crypto"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	"github.com/allinbits/vaas/x/vaas/provider/types"
)

func TestVerifyDoubleVotingEvidence(t *testing.T) {
	keeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	const chainID = "chain-0"

	signer1 := tmtypes.NewMockPV()
	signer2 := tmtypes.NewMockPV()

	val1 := tmtypes.NewValidator(signer1.PrivKey.PubKey(), 1)
	val2 := tmtypes.NewValidator(signer2.PrivKey.PubKey(), 1)

	valSet := tmtypes.NewValidatorSet([]*tmtypes.Validator{val1, val2})

	blockID1 := cryptotestutil.MakeBlockID([]byte("blockhash"), 1000, []byte("partshash"))
	blockID2 := cryptotestutil.MakeBlockID([]byte("blockhash2"), 1000, []byte("partshash"))

	ctx = ctx.WithBlockTime(time.Now())

	valPubkey1, err := cryptocodec.FromCmtPubKeyInterface(val1.PubKey)
	require.NoError(t, err)

	valPubkey2, err := cryptocodec.FromCmtPubKeyInterface(val2.PubKey)
	require.NoError(t, err)

	testCases := []struct {
		name    string
		votes   []*tmtypes.Vote
		chainID string
		pubkey  cryptotypes.PubKey
		expPass bool
	}{
		{
			name: "invalid verifying public key - shouldn't pass",
			votes: []*tmtypes.Vote{
				cryptotestutil.MakeAndSignVote(
					blockID1,
					ctx.BlockHeight(),
					ctx.BlockTime(),
					valSet,
					signer1,
					chainID,
				),
				cryptotestutil.MakeAndSignVote(
					blockID2,
					ctx.BlockHeight(),
					ctx.BlockTime(),
					valSet,
					signer1,
					chainID,
				),
			},
			chainID: chainID,
			pubkey:  nil,
			expPass: false,
		},
		{
			name: "verifying public key doesn't correspond to validator address",
			votes: []*tmtypes.Vote{
				cryptotestutil.MakeAndSignVoteWithForgedValAddress(
					blockID1,
					ctx.BlockHeight(),
					ctx.BlockTime(),
					valSet,
					signer1,
					signer2,
					chainID,
				),
				cryptotestutil.MakeAndSignVoteWithForgedValAddress(
					blockID2,
					ctx.BlockHeight(),
					ctx.BlockTime(),
					valSet,
					signer1,
					signer2,
					chainID,
				),
			},
			chainID: chainID,
			pubkey:  valPubkey1,
			expPass: false,
		},
		{
			name: "evidence has votes with different block height - shouldn't pass",
			votes: []*tmtypes.Vote{
				cryptotestutil.MakeAndSignVote(
					blockID1,
					ctx.BlockHeight()+1,
					ctx.BlockTime(),
					valSet,
					signer1,
					chainID,
				),
				cryptotestutil.MakeAndSignVote(
					blockID2,
					ctx.BlockHeight(),
					ctx.BlockTime(),
					valSet,
					signer1,
					chainID,
				),
			},
			chainID: chainID,
			pubkey:  valPubkey1,
			expPass: false,
		},
		{
			"evidence has votes with different validator address - shouldn't pass",
			[]*tmtypes.Vote{
				cryptotestutil.MakeAndSignVote(
					blockID1,
					ctx.BlockHeight(),
					ctx.BlockTime(),
					valSet,
					signer1,
					chainID,
				),
				cryptotestutil.MakeAndSignVote(
					blockID2,
					ctx.BlockHeight(),
					ctx.BlockTime(),
					valSet,
					signer2,
					chainID,
				),
			},
			chainID,
			valPubkey1,
			false,
		},
		{
			"evidence has votes with same block IDs - shouldn't pass",
			[]*tmtypes.Vote{
				cryptotestutil.MakeAndSignVote(
					blockID1,
					ctx.BlockHeight(),
					ctx.BlockTime(),
					valSet,
					signer1,
					chainID,
				),
				cryptotestutil.MakeAndSignVote(
					blockID1,
					ctx.BlockHeight(),
					ctx.BlockTime(),
					valSet,
					signer1,
					chainID,
				),
			},
			chainID,
			valPubkey1,
			false,
		},
		{
			"given chain ID isn't the same as the one used to sign the votes - shouldn't pass",
			[]*tmtypes.Vote{
				cryptotestutil.MakeAndSignVote(
					blockID1,
					ctx.BlockHeight(),
					ctx.BlockTime(),
					valSet,
					signer1,
					chainID,
				),
				cryptotestutil.MakeAndSignVote(
					blockID2,
					ctx.BlockHeight(),
					ctx.BlockTime(),
					valSet,
					signer1,
					chainID,
				),
			},
			"WrongChainID",
			valPubkey1,
			false,
		},
		{
			"voteA is signed using the wrong chain ID - shouldn't pass",
			[]*tmtypes.Vote{
				cryptotestutil.MakeAndSignVote(
					blockID1,
					ctx.BlockHeight(),
					ctx.BlockTime(),
					valSet,
					signer1,
					"WrongChainID",
				),
				cryptotestutil.MakeAndSignVote(
					blockID2,
					ctx.BlockHeight(),
					ctx.BlockTime(),
					valSet,
					signer1,
					chainID,
				),
			},
			chainID,
			valPubkey1,
			false,
		},
		{
			"voteB is signed using the wrong chain ID - shouldn't pass",
			[]*tmtypes.Vote{
				cryptotestutil.MakeAndSignVote(
					blockID1,
					ctx.BlockHeight(),
					ctx.BlockTime(),
					valSet,
					signer1,
					chainID,
				),
				cryptotestutil.MakeAndSignVote(
					blockID2,
					ctx.BlockHeight(),
					ctx.BlockTime(),
					valSet,
					signer1,
					"WrongChainID",
				),
			},
			chainID,
			valPubkey1,
			false,
		},
		{
			"wrong public key - shouldn't pass",
			[]*tmtypes.Vote{
				cryptotestutil.MakeAndSignVote(
					blockID1,
					ctx.BlockHeight(),
					ctx.BlockTime(),
					valSet,
					signer1,
					chainID,
				),
				cryptotestutil.MakeAndSignVote(
					blockID2,
					ctx.BlockHeight(),
					ctx.BlockTime(),
					valSet,
					signer1,
					chainID,
				),
			},
			chainID,
			valPubkey2,
			false,
		},
		{
			"valid double voting evidence should pass",
			[]*tmtypes.Vote{
				cryptotestutil.MakeAndSignVote(
					blockID1,
					ctx.BlockHeight(),
					ctx.BlockTime(),
					valSet,
					signer1,
					chainID,
				),
				cryptotestutil.MakeAndSignVote(
					blockID2,
					ctx.BlockHeight(),
					ctx.BlockTime(),
					valSet,
					signer1,
					chainID,
				),
			},
			chainID,
			valPubkey1,
			true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err = keeper.VerifyDoubleVotingEvidence(
				tmtypes.DuplicateVoteEvidence{
					VoteA:            tc.votes[0],
					VoteB:            tc.votes[1],
					ValidatorPower:   val1.VotingPower,
					TotalVotingPower: val1.VotingPower,
					Timestamp:        tc.votes[0].Timestamp,
				},
				tc.chainID,
				tc.pubkey,
			)
			if tc.expPass {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

// TestJailAndTombstoneValidator tests that the jailing of a validator is only executed
// under the conditions that the validator is neither unbonded, nor jailed, nor tombstoned.
func TestJailAndTombstoneValidator(t *testing.T) {
	providerConsAddr := cryptotestutil.NewCryptoIdentityFromIntSeed(7842334).ProviderConsAddress()
	testCases := []struct {
		name          string
		provAddr      types.ProviderConsAddress
		expectedCalls func(sdk.Context, testkeeper.MockedKeepers, types.ProviderConsAddress) []any
		err           error
	}{
		{
			name:     "unfound validator",
			provAddr: providerConsAddr,
			expectedCalls: func(ctx sdk.Context, mocks testkeeper.MockedKeepers,
				provAddr types.ProviderConsAddress,
			) []any {
				return []any{
					// We only expect a single call to GetValidatorByConsAddr.
					// Method will return once validator is not found.
					mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(
						ctx, providerConsAddr.ToSdkConsAddr()).Return(
						stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound,
					).Times(1),
				}
			},
			err: slashingtypes.ErrNoValidatorForAddress,
		},
		{
			name:     "unbonded validator",
			provAddr: providerConsAddr,
			expectedCalls: func(ctx sdk.Context, mocks testkeeper.MockedKeepers,
				provAddr types.ProviderConsAddress,
			) []any {
				return []any{
					// We only expect a single call to GetValidatorByConsAddr.
					mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(
						ctx, providerConsAddr.ToSdkConsAddr()).Return(
						stakingtypes.Validator{Status: stakingtypes.Unbonded}, nil,
					).Times(1),
				}
			},
			err: stakingtypes.ErrNoUnbondingDelegation,
		},
		{
			name:     "tombstoned validator",
			provAddr: providerConsAddr,
			expectedCalls: func(ctx sdk.Context, mocks testkeeper.MockedKeepers,
				provAddr types.ProviderConsAddress,
			) []any {
				return []any{
					mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(
						ctx, providerConsAddr.ToSdkConsAddr()).Return(
						stakingtypes.Validator{}, nil,
					).Times(1),
					mocks.MockSlashingKeeper.EXPECT().IsTombstoned(
						ctx, providerConsAddr.ToSdkConsAddr()).Return(
						true,
					).Times(1),
				}
			},
			err: slashingtypes.ErrValidatorTombstoned,
		},
		{
			name:     "jailed validator",
			provAddr: providerConsAddr,
			expectedCalls: func(ctx sdk.Context, mocks testkeeper.MockedKeepers,
				provAddr types.ProviderConsAddress,
			) []any {
				jailEndTime := ctx.BlockTime().Add(getTestInfractionParameters().DoubleSign.JailDuration)
				return []any{
					mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(
						ctx, providerConsAddr.ToSdkConsAddr()).Return(
						stakingtypes.Validator{Jailed: true}, nil,
					).Times(1),
					mocks.MockSlashingKeeper.EXPECT().IsTombstoned(
						ctx, providerConsAddr.ToSdkConsAddr()).Return(
						false,
					).Times(1),
					mocks.MockSlashingKeeper.EXPECT().JailUntil(
						ctx, providerConsAddr.ToSdkConsAddr(), jailEndTime).
						Times(1),
					mocks.MockSlashingKeeper.EXPECT().Tombstone(
						ctx, providerConsAddr.ToSdkConsAddr()).
						Times(1),
				}
			},
		},
		{
			name:     "bonded validator",
			provAddr: providerConsAddr,
			expectedCalls: func(ctx sdk.Context, mocks testkeeper.MockedKeepers,
				provAddr types.ProviderConsAddress,
			) []any {
				jailEndTime := ctx.BlockTime().Add(getTestInfractionParameters().DoubleSign.JailDuration)
				return []any{
					mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(
						ctx, providerConsAddr.ToSdkConsAddr()).Return(
						stakingtypes.Validator{Status: stakingtypes.Bonded}, nil,
					).Times(1),
					mocks.MockSlashingKeeper.EXPECT().IsTombstoned(
						ctx, providerConsAddr.ToSdkConsAddr()).Return(
						false,
					).Times(1),
					mocks.MockStakingKeeper.EXPECT().Jail(
						ctx, providerConsAddr.ToSdkConsAddr()).
						Times(1),
					mocks.MockSlashingKeeper.EXPECT().JailUntil(
						ctx, providerConsAddr.ToSdkConsAddr(), jailEndTime).
						Times(1),
					mocks.MockSlashingKeeper.EXPECT().Tombstone(
						ctx, providerConsAddr.ToSdkConsAddr()).
						Times(1),
				}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(
				t, testkeeper.NewInMemKeeperParams(t))

			// Setup expected mock calls
			gomock.InOrder(tc.expectedCalls(ctx, mocks, tc.provAddr)...)

			// Execute method and assert expected mock calls
			err := providerKeeper.JailAndTombstoneValidator(ctx, tc.provAddr, getTestInfractionParameters().DoubleSign)
			if tc.err != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tc.err)
				return
			}
			require.NoError(t, err)

			ctrl.Finish()
		})
	}
}

// createUndelegation creates an undelegation with `len(initialBalances)` entries
func createUndelegation(initialBalances []int64, completionTimes []time.Time) stakingtypes.UnbondingDelegation {
	var entries []stakingtypes.UnbondingDelegationEntry
	for i, balance := range initialBalances {
		entry := stakingtypes.UnbondingDelegationEntry{
			InitialBalance: math.NewInt(balance),
			CompletionTime: completionTimes[i],
		}
		entries = append(entries, entry)
	}

	return stakingtypes.UnbondingDelegation{Entries: entries}
}

// createRedelegation creates a redelegation with `len(initialBalances)` entries
func createRedelegation(initialBalances []int64, completionTimes []time.Time) stakingtypes.Redelegation {
	var entries []stakingtypes.RedelegationEntry
	for i, balance := range initialBalances {
		entry := stakingtypes.RedelegationEntry{
			InitialBalance: math.NewInt(balance),
			CompletionTime: completionTimes[i],
		}
		entries = append(entries, entry)
	}

	return stakingtypes.Redelegation{Entries: entries}
}

// TestComputePowerToSlash tests that `ComputePowerToSlash` computes the correct power to be slashed based on
// the tokens in non-mature undelegation and redelegation entries, as well as the current power of the validator
func TestComputePowerToSlash(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	// undelegation or redelegation entries with completion time `now` have matured
	now := ctx.BlockHeader().Time
	// undelegation or redelegation entries with completion time one hour in the future have not yet matured
	nowPlus1Hour := now.Add(time.Hour)

	testCases := []struct {
		name           string
		undelegations  []stakingtypes.UnbondingDelegation
		redelegations  []stakingtypes.Redelegation
		power          int64
		powerReduction math.Int
		expectedPower  int64
	}{
		{
			"both undelegations and redelegations 1",
			// 1000 total undelegation tokens
			[]stakingtypes.UnbondingDelegation{
				createUndelegation([]int64{250, 250}, []time.Time{nowPlus1Hour, nowPlus1Hour}),
				createUndelegation([]int64{500}, []time.Time{nowPlus1Hour, nowPlus1Hour}),
			},
			// 1000 total redelegation tokens
			[]stakingtypes.Redelegation{
				createRedelegation([]int64{500}, []time.Time{nowPlus1Hour, nowPlus1Hour}),
				createRedelegation([]int64{250, 250}, []time.Time{nowPlus1Hour, nowPlus1Hour}),
			},
			int64(1000),
			math.NewInt(1),
			int64(2000/1 + 1000),
		},
		{
			"both undelegations and redelegations 2",
			// 2000 total undelegation tokens
			[]stakingtypes.UnbondingDelegation{
				createUndelegation([]int64{250, 250}, []time.Time{nowPlus1Hour, nowPlus1Hour}),
				createUndelegation([]int64{}, []time.Time{}),
				createUndelegation([]int64{100, 100}, []time.Time{nowPlus1Hour, nowPlus1Hour}),
				createUndelegation([]int64{800}, []time.Time{nowPlus1Hour}),
				createUndelegation([]int64{500}, []time.Time{nowPlus1Hour}),
			},
			// 3500 total redelegation tokens
			[]stakingtypes.Redelegation{
				createRedelegation([]int64{}, []time.Time{}),
				createRedelegation([]int64{1600}, []time.Time{nowPlus1Hour}),
				createRedelegation([]int64{350, 250}, []time.Time{nowPlus1Hour, nowPlus1Hour}),
				createRedelegation([]int64{700, 200}, []time.Time{nowPlus1Hour, nowPlus1Hour}),
				createRedelegation([]int64{}, []time.Time{}),
				createRedelegation([]int64{400}, []time.Time{nowPlus1Hour}),
			},
			int64(8391),
			math.NewInt(2),
			int64((2000+3500)/2 + 8391),
		},
		{
			"no undelegations or redelegations, return provided power",
			[]stakingtypes.UnbondingDelegation{},
			[]stakingtypes.Redelegation{},
			int64(3000),
			math.NewInt(5),
			int64(3000), // expectedPower is 0/5 + 3000
		},
		{
			"no undelegations",
			[]stakingtypes.UnbondingDelegation{},
			// 2000 total redelegation tokens
			[]stakingtypes.Redelegation{
				createRedelegation([]int64{}, []time.Time{}),
				createRedelegation([]int64{500}, []time.Time{nowPlus1Hour}),
				createRedelegation([]int64{250, 250}, []time.Time{nowPlus1Hour, nowPlus1Hour}),
				createRedelegation([]int64{700, 200}, []time.Time{nowPlus1Hour, nowPlus1Hour}),
				createRedelegation([]int64{}, []time.Time{}),
				createRedelegation([]int64{100}, []time.Time{nowPlus1Hour}),
			},
			int64(17),
			math.NewInt(3),
			int64(2000/3 + 17),
		},
		{
			"no redelegations",
			// 2000 total undelegation tokens
			[]stakingtypes.UnbondingDelegation{
				createUndelegation([]int64{250, 250}, []time.Time{nowPlus1Hour, nowPlus1Hour}),
				createUndelegation([]int64{}, []time.Time{}),
				createUndelegation([]int64{100, 100}, []time.Time{nowPlus1Hour, nowPlus1Hour}),
				createUndelegation([]int64{800}, []time.Time{nowPlus1Hour}),
				createUndelegation([]int64{500}, []time.Time{nowPlus1Hour}),
			},
			[]stakingtypes.Redelegation{},
			int64(1),
			math.NewInt(3),
			int64(2000/3 + 1),
		},
		{
			"both (mature) undelegations and redelegations",
			// 2000 total undelegation tokens, 250 + 100 + 500 = 850 of those are from mature undelegations,
			// so 2000 - 850 = 1150
			[]stakingtypes.UnbondingDelegation{
				createUndelegation([]int64{250, 250}, []time.Time{nowPlus1Hour, now}),
				createUndelegation([]int64{}, []time.Time{}),
				createUndelegation([]int64{100, 100}, []time.Time{now, nowPlus1Hour}),
				createUndelegation([]int64{800}, []time.Time{nowPlus1Hour}),
				createUndelegation([]int64{500}, []time.Time{now}),
			},
			// 3500 total redelegation tokens, 350 + 200 + 400 = 950 of those are from mature redelegations
			// so 3500 - 950 = 2550
			[]stakingtypes.Redelegation{
				createRedelegation([]int64{}, []time.Time{}),
				createRedelegation([]int64{1600}, []time.Time{nowPlus1Hour}),
				createRedelegation([]int64{350, 250}, []time.Time{now, nowPlus1Hour}),
				createRedelegation([]int64{700, 200}, []time.Time{nowPlus1Hour, now}),
				createRedelegation([]int64{}, []time.Time{}),
				createRedelegation([]int64{400}, []time.Time{now}),
			},
			int64(8391),
			math.NewInt(2),
			int64((1150+2550)/2 + 8391),
		},
	}

	pubKey, _ := cryptocodec.FromCmtPubKeyInterface(tmtypes.NewMockPV().PrivKey.PubKey())
	validator, _ := stakingtypes.NewValidator(pubKey.Address().String(), pubKey, stakingtypes.Description{})

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gomock.InOrder(mocks.MockStakingKeeper.EXPECT().
				SlashUnbondingDelegation(gomock.Any(), gomock.Any(), int64(0), math.LegacyNewDec(1)).
				DoAndReturn(
					func(_ sdk.Context, undelegation stakingtypes.UnbondingDelegation, _ int64, _ math.LegacyDec) (math.Int, error) {
						sum := math.NewInt(0)
						for _, r := range undelegation.Entries {
							if r.IsMature(ctx.BlockTime()) {
								continue
							}
							sum = sum.Add(math.NewInt(r.InitialBalance.Int64()))
						}
						return sum, nil
					}).AnyTimes(),
				mocks.MockStakingKeeper.EXPECT().
					SlashRedelegation(gomock.Any(), gomock.Any(), gomock.Any(), int64(0), math.LegacyNewDec(1)).
					DoAndReturn(
						func(ctx sdk.Context, _ stakingtypes.Validator, redelegation stakingtypes.Redelegation, _ int64, _ math.LegacyDec) (math.Int, error) {
							sum := math.NewInt(0)
							for _, r := range redelegation.Entries {
								if r.IsMature(ctx.BlockTime()) {
									continue
								}
								sum = sum.Add(math.NewInt(r.InitialBalance.Int64()))
							}
							return sum, nil
						}).AnyTimes(),
			)

			actualPower := providerKeeper.ComputePowerToSlash(ctx, validator,
				tc.undelegations, tc.redelegations, tc.power, tc.powerReduction)

			if tc.expectedPower != actualPower {
				require.Fail(t, fmt.Sprintf("\"%s\" failed", tc.name),
					"expected is %d but actual is %d", tc.expectedPower, actualPower)
			}
		})
	}
}

// TestSlashValidator asserts that `SlashValidator` calls the staking module's `Slash` method
// with the correct arguments (i.e., `infractionHeight` of 0 and the expected slash power)
func TestSlashValidator(t *testing.T) {
	keeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	// undelegation or redelegation entries with completion time `now` have matured
	now := ctx.BlockHeader().Time
	// undelegation or redelegation entries with completion time one hour in the future have not yet matured
	nowPlus1Hour := now.Add(time.Hour)

	pubKey, _ := cryptocodec.FromCmtPubKeyInterface(tmtypes.NewMockPV().PrivKey.PubKey())

	validator, err := stakingtypes.NewValidator(
		sdk.ValAddress(pubKey.Address()).String(),
		pubKey,
		stakingtypes.NewDescription("", "", "", "", ""),
	)
	require.NoError(t, err)
	validator.Status = stakingtypes.Bonded

	consAddr, _ := validator.GetConsAddr()
	providerAddr := types.NewProviderConsAddress(consAddr)

	// we create 1000 tokens worth of undelegations, 750 of them are non-matured
	// we also create 1000 tokens worth of redelegations, 750 of them are non-matured
	undelegations := []stakingtypes.UnbondingDelegation{
		createUndelegation([]int64{250, 250}, []time.Time{nowPlus1Hour, now}),
		createUndelegation([]int64{500}, []time.Time{nowPlus1Hour}),
	}
	redelegations := []stakingtypes.Redelegation{
		createRedelegation([]int64{250, 250}, []time.Time{now, nowPlus1Hour}),
		createRedelegation([]int64{500}, []time.Time{nowPlus1Hour}),
	}

	// validator's current power
	currentPower := int64(3000)

	powerReduction := math.NewInt(2)
	slashFraction := getTestInfractionParameters().DoubleSign.SlashFraction

	// the call to `Slash` should provide an `infractionHeight` of 0 and an expected power of
	// (750 (undelegations) + 750 (redelegations)) / 2 (= powerReduction) + 3000 (currentPower) = 3750
	expectedInfractionHeight := int64(0)
	expectedSlashPower := int64(3750)

	expectedValoperAddr, err := keeper.ValidatorAddressCodec().StringToBytes(validator.GetOperator())
	require.NoError(t, err)

	expectedCalls := []any{
		mocks.MockStakingKeeper.EXPECT().
			GetValidatorByConsAddr(ctx, gomock.Any()).
			Return(validator, nil),
		mocks.MockSlashingKeeper.EXPECT().
			IsTombstoned(ctx, consAddr).
			Return(false),
		mocks.MockStakingKeeper.EXPECT().
			GetUnbondingDelegationsFromValidator(ctx, expectedValoperAddr).
			Return(undelegations, nil),
		mocks.MockStakingKeeper.EXPECT().
			GetRedelegationsFromSrcValidator(ctx, expectedValoperAddr).
			Return(redelegations, nil),
		mocks.MockStakingKeeper.EXPECT().
			GetLastValidatorPower(ctx, expectedValoperAddr).
			Return(currentPower, nil),
		mocks.MockStakingKeeper.EXPECT().
			PowerReduction(ctx).
			Return(powerReduction),
		mocks.MockStakingKeeper.EXPECT().
			SlashUnbondingDelegation(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(_ sdk.Context, undelegation stakingtypes.UnbondingDelegation, _ int64, _ math.LegacyDec) (math.Int, error) {
					sum := math.NewInt(0)
					for _, r := range undelegation.Entries {
						if r.IsMature(ctx.BlockTime()) {
							continue
						}
						sum = sum.Add(math.NewInt(r.InitialBalance.Int64()))
					}
					return sum, nil
				}).AnyTimes(),
		mocks.MockStakingKeeper.EXPECT().
			SlashRedelegation(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(_ sdk.Context, _ stakingtypes.Validator, redelegation stakingtypes.Redelegation, _ int64, _ math.LegacyDec) (math.Int, error) {
					sum := math.NewInt(0)
					for _, r := range redelegation.Entries {
						if r.IsMature(ctx.BlockTime()) {
							continue
						}
						sum = sum.Add(math.NewInt(r.InitialBalance.Int64()))
					}
					return sum, nil
				}).AnyTimes(),
		mocks.MockStakingKeeper.EXPECT().
			SlashWithInfractionReason(ctx, consAddr, expectedInfractionHeight, expectedSlashPower, slashFraction, stakingtypes.Infraction_INFRACTION_DOUBLE_SIGN).Return(math.NewInt(expectedSlashPower), nil).
			Times(1),
	}

	gomock.InOrder(expectedCalls...)
	err = keeper.SlashValidator(ctx, providerAddr, getTestInfractionParameters().DoubleSign, stakingtypes.Infraction_INFRACTION_DOUBLE_SIGN)
	require.NoError(t, err)
}

// TestSlashValidatorDoesNotSlashIfValidatorIsUnbonded asserts that `SlashValidator` does not call
// the staking module's `Slash` method if the validator to be slashed is unbonded
func TestSlashValidatorDoesNotSlashIfValidatorIsUnbonded(t *testing.T) {
	keeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	keeperParams := testkeeper.NewInMemKeeperParams(t)
	testkeeper.NewInMemProviderKeeper(keeperParams, mocks)

	pubKey, _ := cryptocodec.FromCmtPubKeyInterface(tmtypes.NewMockPV().PrivKey.PubKey())

	// validator is initially unbonded
	validator, _ := stakingtypes.NewValidator(pubKey.Address().String(), pubKey, stakingtypes.Description{})

	consAddr, _ := validator.GetConsAddr()
	providerAddr := types.NewProviderConsAddress(consAddr)

	expectedCalls := []any{
		mocks.MockStakingKeeper.EXPECT().
			GetValidatorByConsAddr(ctx, gomock.Any()).
			Return(validator, nil),
	}

	gomock.InOrder(expectedCalls...)
	err := keeper.SlashValidator(ctx, providerAddr, getTestInfractionParameters().DoubleSign, stakingtypes.Infraction_INFRACTION_DOUBLE_SIGN)
	require.Error(t, err)
	require.ErrorIs(t, stakingtypes.ErrNoUnbondingDelegation, err)
}

func TestEquivocationEvidenceMinHeightCRUD(t *testing.T) {
	consumerID := CONSUMER_ID
	expMinHeight := uint64(12)
	keeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	height := keeper.GetEquivocationEvidenceMinHeight(ctx, consumerID)
	require.Zero(t, height, "equivocation evidence min height should be 0")

	keeper.SetEquivocationEvidenceMinHeight(ctx, consumerID, expMinHeight)
	height = keeper.GetEquivocationEvidenceMinHeight(ctx, consumerID)
	require.Equal(t, height, expMinHeight)

	keeper.DeleteEquivocationEvidenceMinHeight(ctx, consumerID)
	height = keeper.GetEquivocationEvidenceMinHeight(ctx, consumerID)
	require.Zero(t, height, "equivocation evidence min height should be 0")
}

func getTestInfractionParameters() *types.InfractionParameters {
	return &types.InfractionParameters{
		DoubleSign: &types.SlashJailParameters{
			JailDuration:  1200 * time.Second,
			SlashFraction: math.LegacyNewDecWithPrec(5, 1), // 0.5
			Tombstone:     true,
		},
		Downtime: &types.SlashJailParameters{
			JailDuration:  600 * time.Second,
			SlashFraction: math.LegacyNewDecWithPrec(5, 4),
			Tombstone:     false,
		},
	}
}

// legacyDecEq matches a math.LegacyDec argument by numeric value (LegacyDec.Equal)
// rather than reflect.DeepEqual, so a fraction read back from the params store
// still matches regardless of the underlying big.Int representation.
type legacyDecEq struct{ expected math.LegacyDec }

func (m legacyDecEq) Matches(x any) bool {
	d, ok := x.(math.LegacyDec)
	return ok && d.Equal(m.expected)
}

func (m legacyDecEq) String() string {
	return fmt.Sprintf("is numerically equal to LegacyDec %s", m.expected)
}

// TestHandleConfirmedLightClientAttackTombstonesByzantineValidators asserts that
// a confirmed light-client attack with an identifiable byzantine set is punished
// exactly like vote-level double signing: each signer is slashed at the
// DoubleSign fraction, jailed, and tombstoned. The consumer keeps running --
// the chain-level escalation only fires when nobody can be punished.
func TestHandleConfirmedLightClientAttackTombstonesByzantineValidators(t *testing.T) {
	keeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	ctx = ctx.WithBlockTime(time.Now())

	const consumerID = uint64(0)
	keeper.SetConsumerPhase(ctx, consumerID, types.CONSUMER_PHASE_LAUNCHED)
	keeper.SetInfractionParams(ctx, types.DefaultInfractionParameters())
	doubleSign := keeper.GetInfractionParams(ctx).DoubleSign
	require.True(t, doubleSign.Tombstone, "double-sign params must tombstone")

	// Build a byzantine validator. With no assigned consumer key, its consumer
	// consensus address is its provider consensus address.
	byzantinePV := tmtypes.NewMockPV()
	byzantineVal := tmtypes.NewValidator(byzantinePV.PrivKey.PubKey(), 1)

	sdkPubKey, err := cryptocodec.FromCmtPubKeyInterface(byzantineVal.PubKey)
	require.NoError(t, err)
	validator, err := stakingtypes.NewValidator(
		sdk.ValAddress(sdkPubKey.Address()).String(),
		sdkPubKey,
		stakingtypes.NewDescription("", "", "", "", ""),
	)
	require.NoError(t, err)
	validator.Status = stakingtypes.Bonded

	consAddr, err := validator.GetConsAddr()
	require.NoError(t, err)
	providerAddr := types.NewProviderConsAddress(consAddr)
	valAddr, err := keeper.ValidatorAddressCodec().StringToBytes(validator.GetOperator())
	require.NoError(t, err)

	// SlashValidator and JailAndTombstoneValidator each look the validator up
	// and check its tombstone flag once.
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx, consAddr).Return(validator, nil).Times(2)
	mocks.MockSlashingKeeper.EXPECT().IsTombstoned(ctx, consAddr).Return(false).Times(2)

	// Slash must use the DoubleSign fraction and the double-sign infraction reason.
	mocks.MockStakingKeeper.EXPECT().GetUnbondingDelegationsFromValidator(ctx, valAddr).Return(nil, nil)
	mocks.MockStakingKeeper.EXPECT().GetRedelegationsFromSrcValidator(ctx, valAddr).Return(nil, nil)
	mocks.MockStakingKeeper.EXPECT().GetLastValidatorPower(ctx, valAddr).Return(int64(500), nil)
	mocks.MockStakingKeeper.EXPECT().PowerReduction(ctx).Return(math.NewInt(1))
	mocks.MockStakingKeeper.EXPECT().SlashWithInfractionReason(
		ctx, consAddr, int64(0), int64(500), legacyDecEq{doubleSign.SlashFraction}, stakingtypes.Infraction_INFRACTION_DOUBLE_SIGN,
	).Return(math.NewInt(25), nil)

	// Jail and tombstone.
	mocks.MockStakingKeeper.EXPECT().Jail(ctx, consAddr)
	mocks.MockSlashingKeeper.EXPECT().JailUntil(ctx, consAddr, gomock.Any())
	mocks.MockSlashingKeeper.EXPECT().Tombstone(ctx, consAddr)

	punished, err := keeper.SlashOrEscalateLightClientAttackForTest(ctx, consumerID, []*tmtypes.Validator{byzantineVal})
	require.NoError(t, err)
	require.Equal(t, []types.ProviderConsAddress{providerAddr}, punished)

	// Punishing the byzantine set does not stop the chain.
	require.Equal(t, types.CONSUMER_PHASE_LAUNCHED, keeper.GetConsumerPhase(ctx, consumerID))
}

// TestHandleConfirmedLightClientAttackEscalatesWhenUnidentifiable asserts that a
// confirmed attack with no identifiable byzantine validators (an amnesia
// attack) is not a silent no-op: the consumer is stopped and scheduled for
// removal. Re-submitting the same evidence does not schedule removal twice.
func TestHandleConfirmedLightClientAttackEscalatesWhenUnidentifiable(t *testing.T) {
	keeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	ctx = ctx.WithBlockTime(time.Now())
	keeper.SetInfractionParams(ctx, types.DefaultInfractionParameters())

	unbonding := 21 * 24 * time.Hour
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(unbonding, nil).AnyTimes()

	const consumerID = uint64(0)
	keeper.SetConsumerPhase(ctx, consumerID, types.CONSUMER_PHASE_LAUNCHED)

	// No byzantine validators can be extracted: no slash/jail mocks are set, so
	// ctrl.Finish() also asserts that no validator was punished.
	punished, err := keeper.SlashOrEscalateLightClientAttackForTest(ctx, consumerID, nil)
	require.NoError(t, err)
	require.Empty(t, punished)

	require.Equal(t, types.CONSUMER_PHASE_STOPPED, keeper.GetConsumerPhase(ctx, consumerID))
	removalTime, err := keeper.GetConsumerRemovalTime(ctx, consumerID)
	require.NoError(t, err)
	require.Equal(t, ctx.BlockTime().Add(unbonding), removalTime)

	toRemove, err := keeper.GetConsumersToBeRemoved(ctx, removalTime)
	require.NoError(t, err)
	require.Contains(t, toRemove.Ids, consumerID)

	// Re-submitting the same unattributable attack is a no-op: the consumer is
	// already stopping, so it is not appended to the removal queue again.
	punished, err = keeper.SlashOrEscalateLightClientAttackForTest(ctx, consumerID, nil)
	require.NoError(t, err)
	require.Empty(t, punished)

	toRemove, err = keeper.GetConsumersToBeRemoved(ctx, removalTime)
	require.NoError(t, err)
	require.Len(t, toRemove.Ids, 1)
}

// TestHandleConfirmedLightClientAttackEscalatesWhenSignersUnpunishable asserts
// that when a byzantine signer is identified but cannot be punished (here:
// already unbonded), the confirmed attack still escalates to stopping the
// consumer rather than doing nothing.
func TestHandleConfirmedLightClientAttackEscalatesWhenSignersUnpunishable(t *testing.T) {
	keeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	ctx = ctx.WithBlockTime(time.Now())
	keeper.SetInfractionParams(ctx, types.DefaultInfractionParameters())
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).AnyTimes()

	const consumerID = uint64(0)
	keeper.SetConsumerPhase(ctx, consumerID, types.CONSUMER_PHASE_LAUNCHED)

	byzantinePV := tmtypes.NewMockPV()
	byzantineVal := tmtypes.NewValidator(byzantinePV.PrivKey.PubKey(), 1)
	sdkPubKey, err := cryptocodec.FromCmtPubKeyInterface(byzantineVal.PubKey)
	require.NoError(t, err)
	consAddr := sdk.ConsAddress(sdkPubKey.Address())

	// SlashValidator finds the validator already unbonded and refuses to slash;
	// with nobody punishable the attack escalates to the chain level.
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx, consAddr).
		Return(stakingtypes.Validator{Status: stakingtypes.Unbonded}, nil)

	punished, err := keeper.SlashOrEscalateLightClientAttackForTest(ctx, consumerID, []*tmtypes.Validator{byzantineVal})
	require.NoError(t, err)
	require.Empty(t, punished)
	require.Equal(t, types.CONSUMER_PHASE_STOPPED, keeper.GetConsumerPhase(ctx, consumerID))
}
