package keeper_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	tmtypes "github.com/cometbft/cometbft/types"

	cryptotestutil "github.com/allinbits/vaas/testutil/crypto"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
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

// setupDowntimeTest builds a launched consumer with a registered client and
// validator, wires infractionParams and spawnTime, and overrides
// windowEndTimestamp resolution to return windowEndTime (see
// Keeper.OverrideWindowEndTimestampForTest) -- so tests exercising the
// grace-period and evidence-age checks don't need to fabricate real IBC
// consensus states. Returns the keeper, context, mock controller, mocks, the
// consumer id, and the validator's provider consensus address.
func setupDowntimeTest(
	t *testing.T,
	infractionParams types.InfractionParameters,
	spawnTime time.Time,
	windowEndTime time.Time,
) (providerkeeper.Keeper, sdk.Context, *gomock.Controller, testkeeper.MockedKeepers, uint64, types.ProviderConsAddress) {
	t.Helper()
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)

	consumerId := uint64(0)
	providerKeeper.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-chain")
	providerKeeper.SetConsumerClientId(ctx, consumerId, "07-tendermint-0")
	providerKeeper.SetEquivocationEvidenceMinHeight(ctx, consumerId, 1)
	providerKeeper.SetInfractionParams(ctx, infractionParams)
	require.NoError(t, providerKeeper.SetConsumerInitializationParameters(ctx, consumerId, types.ConsumerInitializationParameters{
		SpawnTime: spawnTime,
	}))

	pubKey, err := cryptocodec.FromCmtPubKeyInterface(tmtypes.NewMockPV().PrivKey.PubKey())
	require.NoError(t, err)
	validator, err := stakingtypes.NewValidator(
		sdk.ValAddress(pubKey.Address()).String(),
		pubKey,
		stakingtypes.NewDescription("", "", "", "", ""),
	)
	require.NoError(t, err)
	consAddr, err := validator.GetConsAddr()
	require.NoError(t, err)
	providerAddr := types.NewProviderConsAddress(consAddr)
	cmtPubKey, err := validator.CmtConsPublicKey()
	require.NoError(t, err)
	require.NoError(t, providerKeeper.SetConsumerValidator(ctx, consumerId, types.ConsensusValidator{
		ProviderConsAddr: consAddr,
		Power:            1000,
		PublicKey:        &cmtPubKey,
		JoinHeight:       1,
	}))

	providerKeeper.OverrideWindowEndTimestampForTest(func(_ sdk.Context, _ string, _ int64) (time.Time, error) {
		return windowEndTime, nil
	})

	return providerKeeper, ctx, ctrl, mocks, consumerId, providerAddr
}

// downtimeParams returns InfractionParameters carrying only the fields
// HandleConsumerDowntime reads; DoubleSign/Downtime slash-jail params are
// left nil since the downtime path no longer slashes directly (it queues a
// PendingDowntimeSlash instead).
func downtimeParams(window int64, minSigned string, gracePeriod, challengeWindow, maxAge time.Duration) types.InfractionParameters {
	return types.InfractionParameters{
		SignedBlocksWindow:      window,
		MinSignedPerWindow:      math.LegacyMustNewDecFromStr(minSigned),
		DowntimeGracePeriod:     gracePeriod,
		DowntimeChallengeWindow: challengeWindow,
		DowntimeEvidenceMaxAge:  maxAge,
	}
}

func TestHandleConsumerDowntimeAcceptsAndQueuesPricedSlash(t *testing.T) {
	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	spawnTime := windowEndTime.Add(-30 * 24 * time.Hour)
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)

	providerKeeper, ctx, ctrl, mocks, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, windowEndTime)
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(windowEndTime)

	// P=1000uphoton, resolved from an epoch record covering this window; C=2.
	providerKeeper.SetEpochShareRecord(ctx, consumerId, windowEndTime, math.NewInt(1000))
	mocks.MockPhotonKeeper.EXPECT().ConversionRate(ctx).Return(math.LegacyNewDec(2), nil)

	consAddr := providerAddr.ToSdkConsAddr()
	evidencePacket := vaastypes.NewEvidencePacketData(
		sdk.ConsAddress(consAddr),
		93,
		[]byte{0x3F}, // 6 of 8 missed; maxMissed for window=8,minSigned=0.5 is 4
		8,
		8,
		math.LegacyMustNewDecFromStr("0.5"),
	)

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.NoError(t, err)

	require.True(t, providerKeeper.IsEpochDowntime(ctx, consumerId, consAddr))

	pending, err := providerKeeper.PendingDowntimeSlashes.Get(ctx, collections.Join(consumerId, consAddr.Bytes()))
	require.NoError(t, err)
	require.Equal(t, int64(93), pending.WindowStartHeight)
	require.Equal(t, int64(8), pending.Span)
	require.Equal(t, int64(6), pending.MissedCount)
	// M = 6/8 = 0.75; slashTokens = P*M/C = 1000*0.75/2 = 375
	require.True(t, math.NewInt(375).Equal(pending.SlashTokens), "expected 375, got %s", pending.SlashTokens)
	require.Equal(t, windowEndTime.Add(7*24*time.Hour), pending.MaturesAt)
}

func TestHandleConsumerDowntimeRejectsBelowThreshold(t *testing.T) {
	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	spawnTime := windowEndTime.Add(-30 * 24 * time.Hour)
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)

	providerKeeper, ctx, ctrl, _, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, windowEndTime)
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(windowEndTime)

	evidencePacket := vaastypes.NewEvidencePacketData(
		sdk.ConsAddress(providerAddr.ToSdkConsAddr()),
		93,
		[]byte{0x07}, // 3 of 8 missed; maxMissed is 4, so this does not qualify
		8,
		8,
		math.LegacyMustNewDecFromStr("0.5"),
	)

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not exceed the infraction threshold")
}

func TestHandleConsumerEvidencePacketRejectsSpanExceedingWindow(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	consumerId := uint64(0)
	providerKeeper.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_LAUNCHED)

	// Span (8) exceeds the echoed window (4); rejected by
	// EvidencePacketData.Validate() before HandleConsumerDowntime ever runs.
	evidencePacket := vaastypes.EvidencePacketData{
		ValidatorAddr:      sdk.ConsAddress([]byte{0x01, 0x02, 0x03}),
		InfractionHeight:   107,
		Infraction:         stakingtypes.Infraction_INFRACTION_DOWNTIME,
		WindowStartHeight:  100,
		MissedBlocksBitmap: []byte{0xFF},
		SignedBlocksWindow: 4,
		MinSignedPerWindow: math.LegacyMustNewDecFromStr("0.5"),
	}

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
	require.Contains(t, err.Error(), "window span cannot exceed")
}

func TestHandleConsumerDowntimeRejectsUnacceptableParams(t *testing.T) {
	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	spawnTime := windowEndTime.Add(-30 * 24 * time.Hour)
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)

	providerKeeper, ctx, ctrl, _, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, windowEndTime)
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(windowEndTime)

	// Echoes a window (10) that matches neither the current params (8) nor
	// any recorded previous params.
	evidencePacket := vaastypes.NewEvidencePacketData(
		sdk.ConsAddress(providerAddr.ToSdkConsAddr()),
		93,
		[]byte{0x3F},
		8,
		10,
		math.LegacyMustNewDecFromStr("0.5"),
	)

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unacceptable downtime params")
}

func TestHandleConsumerDowntimeRejectsDuplicatePending(t *testing.T) {
	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	spawnTime := windowEndTime.Add(-30 * 24 * time.Hour)
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)

	providerKeeper, ctx, ctrl, mocks, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, windowEndTime)
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(windowEndTime)

	providerKeeper.SetEpochShareRecord(ctx, consumerId, windowEndTime, math.NewInt(1000))
	mocks.MockPhotonKeeper.EXPECT().ConversionRate(ctx).Return(math.LegacyNewDec(2), nil)

	consAddr := providerAddr.ToSdkConsAddr()
	firstPacket := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, firstPacket))

	// A second, distinct window for the same (consumer, validator) is
	// rejected while the first slash is still pending; ConversionRate is not
	// called again (verified by the single EXPECT() above and ctrl.Finish()).
	secondPacket := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 101, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, secondPacket)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already pending")
}

func TestHandleConsumerDowntimeRejectsOverlappingPunishedWindow(t *testing.T) {
	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	spawnTime := windowEndTime.Add(-30 * 24 * time.Hour)
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)

	providerKeeper, ctx, ctrl, mocks, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, windowEndTime)
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(windowEndTime)

	providerKeeper.SetEpochShareRecord(ctx, consumerId, windowEndTime, math.NewInt(1000))
	mocks.MockPhotonKeeper.EXPECT().ConversionRate(ctx).Return(math.LegacyNewDec(2), nil).Times(2)

	consAddr := providerAddr.ToSdkConsAddr()
	pendingKey := collections.Join(consumerId, consAddr.Bytes())

	// windowStart 93, span 8 => InfractionHeight (window end) 100.
	firstPacket := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, firstPacket))

	// Simulate the pending slash maturing and executing: the epoch sweep
	// removes the PendingDowntimeSlashes entry (see SweepPendingDowntimeSlashes),
	// but LastPunishedWindowEnds must retain the window-end height so a
	// re-submission for the same or an overlapping window is still caught.
	require.NoError(t, providerKeeper.PendingDowntimeSlashes.Remove(ctx, pendingKey))

	// An exact duplicate of the punished window (start 93 <= last punished
	// end 100) is rejected even though nothing is pending anymore.
	duplicate := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, duplicate)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already punished")

	// A window overlapping the punished one (start 95 <= 100) is likewise
	// rejected.
	overlapping := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 95, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	err = providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, overlapping)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already punished")

	// A later, disjoint window (start 101 > 100) is accepted.
	later := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 101, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, later))
}

func TestHandleConsumerDowntimeRejectsStaleEvidence(t *testing.T) {
	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	spawnTime := windowEndTime.Add(-30 * 24 * time.Hour)
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, time.Hour)

	providerKeeper, ctx, ctrl, _, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, windowEndTime)
	defer ctrl.Finish()
	// The evidence is submitted 2h after the window ended; max age is 1h.
	ctx = ctx.WithBlockTime(windowEndTime.Add(2 * time.Hour))

	evidencePacket := vaastypes.NewEvidencePacketData(sdk.ConsAddress(providerAddr.ToSdkConsAddr()), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
	require.Contains(t, err.Error(), "too old: window ended")
}

func TestHandleConsumerDowntimeRejectsDuringGracePeriod(t *testing.T) {
	spawnTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	gracePeriod := 24 * time.Hour
	windowEndTime := spawnTime.Add(12 * time.Hour) // still within grace period
	infractionParams := downtimeParams(8, "0.5", gracePeriod, 7*24*time.Hour, 72*time.Hour)

	providerKeeper, ctx, ctrl, _, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, windowEndTime)
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(windowEndTime)

	evidencePacket := vaastypes.NewEvidencePacketData(sdk.ConsAddress(providerAddr.ToSdkConsAddr()), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
	require.Contains(t, err.Error(), "grace period")
}

func TestHandleConsumerDowntimeGracePeriodDisabledSkipsCheck(t *testing.T) {
	spawnTime := time.Now()
	windowEndTime := spawnTime // would still be "in grace period" if the check ran
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)

	providerKeeper, ctx, ctrl, mocks, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, windowEndTime)
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(windowEndTime)

	providerKeeper.SetEpochShareRecord(ctx, consumerId, windowEndTime, math.NewInt(1000))
	mocks.MockPhotonKeeper.EXPECT().ConversionRate(ctx).Return(math.LegacyNewDec(2), nil)

	evidencePacket := vaastypes.NewEvidencePacketData(sdk.ConsAddress(providerAddr.ToSdkConsAddr()), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.NoError(t, err)
}

func TestHandleConsumerDowntimeRejectsUnanchorableWindow(t *testing.T) {
	spawnTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)

	providerKeeper, ctx, ctrl, _, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, time.Time{})
	defer ctrl.Finish()

	providerKeeper.OverrideWindowEndTimestampForTest(func(_ sdk.Context, _ string, _ int64) (time.Time, error) {
		return time.Time{}, fmt.Errorf("cannot anchor evidence window time")
	})

	evidencePacket := vaastypes.NewEvidencePacketData(sdk.ConsAddress(providerAddr.ToSdkConsAddr()), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot anchor")
}

func TestHandleConsumerDowntimeRejectsTooOldEvidence(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	consumerId := uint64(0)
	providerKeeper.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-chain")
	providerKeeper.SetEquivocationEvidenceMinHeight(ctx, consumerId, 200)
	providerKeeper.SetInfractionParams(ctx, downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour))

	evidencePacket := vaastypes.NewEvidencePacketData(
		sdk.ConsAddress([]byte{0x01, 0x02, 0x03}),
		93, // infraction height 100, below min height of 200
		[]byte{0x3F},
		8,
		8,
		math.LegacyMustNewDecFromStr("0.5"),
	)

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
	require.Contains(t, err.Error(), "too old")
}

func TestHandleConsumerDowntimeRejectsNoClient(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	consumerId := uint64(0)
	providerKeeper.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-chain")
	providerKeeper.SetEquivocationEvidenceMinHeight(ctx, consumerId, 1)
	providerKeeper.SetInfractionParams(ctx, downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour))

	pubKey, _ := cryptocodec.FromCmtPubKeyInterface(tmtypes.NewMockPV().PrivKey.PubKey())
	validator, err := stakingtypes.NewValidator(sdk.ValAddress(pubKey.Address()).String(), pubKey, stakingtypes.NewDescription("", "", "", "", ""))
	require.NoError(t, err)
	consAddr, _ := validator.GetConsAddr()
	cmtPubKey, _ := validator.CmtConsPublicKey()
	require.NoError(t, providerKeeper.SetConsumerValidator(ctx, consumerId, types.ConsensusValidator{
		ProviderConsAddr: consAddr,
		Power:            1000,
		PublicKey:        &cmtPubKey,
		JoinHeight:       1,
	}))

	// No client id is registered for this consumer.
	evidencePacket := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))

	err = providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no IBC client found")
}

func TestHandleConsumerDowntimeRejectsValidatorNotInSet(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	consumerId := uint64(0)
	providerKeeper.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-chain")
	providerKeeper.SetConsumerClientId(ctx, consumerId, "07-tendermint-0")
	providerKeeper.SetEquivocationEvidenceMinHeight(ctx, consumerId, 1)
	providerKeeper.SetInfractionParams(ctx, downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour))

	// The reporting validator is never registered in the consumer's
	// validator set.
	evidencePacket := vaastypes.NewEvidencePacketData(sdk.ConsAddress([]byte{0x01, 0x02, 0x03}), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in the validator set")
}

func TestHandleConsumerEvidencePacketRejectsDoubleSign(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	consumerId := uint64(0)
	providerKeeper.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_LAUNCHED)

	evidencePacket := vaastypes.EvidencePacketData{
		ValidatorAddr:      sdk.ConsAddress([]byte{0x01, 0x02, 0x03}),
		InfractionHeight:   100,
		Infraction:         stakingtypes.Infraction_INFRACTION_DOUBLE_SIGN,
		WindowStartHeight:  100,
		MissedBlocksBitmap: []byte{0x01},
		SignedBlocksWindow: 600,
		MinSignedPerWindow: math.LegacyMustNewDecFromStr("0.5"),
	}

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
}

func TestHandleConsumerEvidencePacketRejectsNonLaunchedConsumer(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	consumerId := uint64(0)
	providerKeeper.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_REGISTERED)

	evidencePacket := vaastypes.NewEvidencePacketData(
		sdk.ConsAddress([]byte{0x01, 0x02, 0x03}),
		100,
		[]byte{0x01},
		1,
		600,
		math.LegacyMustNewDecFromStr("0.5"),
	)

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
}

// TestHandleConsumerEvidencePacketRejectsPausedConsumer verifies that a paused
// consumer's evidence packets are rejected just like any other non-launched
// phase: while a consumer is paused there is no active downtime challenge
// window to accept new evidence into.
func TestHandleConsumerEvidencePacketRejectsPausedConsumer(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	consumerId := uint64(0)
	providerKeeper.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_PAUSED)

	evidencePacket := vaastypes.NewEvidencePacketData(
		sdk.ConsAddress([]byte{0x01, 0x02, 0x03}),
		100,
		[]byte{0x01},
		1,
		600,
		math.LegacyMustNewDecFromStr("0.5"),
	)

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
}

func TestEvidencePacketDataJSONRoundTrip(t *testing.T) {
	addr := sdk.ConsAddress([]byte{0x01, 0x02, 0x03, 0x04, 0x05})
	packet := vaastypes.NewEvidencePacketData(addr, 42, []byte{0x01}, 1, 600, math.LegacyMustNewDecFromStr("0.5"))

	bz := packet.GetBytes()

	var decoded vaastypes.EvidencePacketData
	err := json.Unmarshal(bz, &decoded)
	require.NoError(t, err)
	require.Equal(t, packet.ValidatorAddr, decoded.ValidatorAddr)
	require.Equal(t, packet.InfractionHeight, decoded.InfractionHeight)
	require.Equal(t, packet.Infraction, decoded.Infraction)
}
