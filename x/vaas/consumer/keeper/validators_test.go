package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/math"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	abci "github.com/cometbft/cometbft/abci/types"
	tmrand "github.com/cometbft/cometbft/libs/rand"
	tmtypes "github.com/cometbft/cometbft/types"

	"github.com/allinbits/vaas/testutil/crypto"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	"github.com/allinbits/vaas/x/vaas/consumer/keeper"
	"github.com/allinbits/vaas/x/vaas/consumer/types"
)

// TestApplyCCValidatorChanges tests the ApplyCCValidatorChanges method for a consumer keeper
func TestApplyCCValidatorChanges(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	// utility functions
	getCCVals := func() (vals []types.CrossChainValidator) {
		vals = consumerKeeper.GetAllCCValidator(ctx)
		return
	}

	clearCCVals := func() {
		ccVals := consumerKeeper.GetAllCCValidator(ctx)
		for _, v := range ccVals {
			consumerKeeper.DeleteCCValidator(ctx, v.Address)
		}
	}

	sumCCValsPow := func(vals []types.CrossChainValidator) (power int64) {
		for _, v := range vals {
			power += v.Power
		}
		return
	}

	// prepare the testing setup by clearing the current cross-chain validators in states
	clearCCVals()

	tcValidators := GenerateValidators(t)

	changes := []abci.ValidatorUpdate{}
	changesPower := int64(0)

	for _, v := range tcValidators {
		changes = append(changes, tmtypes.TM2PB.ValidatorUpdate(v))
		changesPower += v.VotingPower
	}

	// finish setup by storing 3 out 4 testing validators as cross-chain validator records
	SetCCValidators(t, consumerKeeper, ctx, tcValidators[:len(tcValidators)-1])

	// verify setup
	ccVals := getCCVals()
	require.Len(t, ccVals, len(tcValidators)-1)

	// test behaviors
	testCases := []struct {
		name          string
		changes       []abci.ValidatorUpdate
		expTotalPower int64
		expValsNum    int
	}{
		{
			name:          "add new bonded validator",
			changes:       changes[len(changes)-1:],
			expTotalPower: changesPower,
			expValsNum:    len(ccVals) + 1,
		},
		{
			name:          "update a validator voting power",
			changes:       []abci.ValidatorUpdate{{PubKey: changes[0].PubKey, Power: changes[0].Power + 3}},
			expTotalPower: changesPower + 3,
			expValsNum:    len(ccVals) + 1,
		},
		{
			name:          "unbond a validator",
			changes:       []abci.ValidatorUpdate{{PubKey: changes[0].PubKey, Power: 0}},
			expTotalPower: changesPower - changes[0].Power,
			expValsNum:    len(ccVals),
		},
		{
			name: "update all validators voting power",
			changes: []abci.ValidatorUpdate{
				{PubKey: changes[0].PubKey, Power: changes[0].Power + 1},
				{PubKey: changes[1].PubKey, Power: changes[1].Power + 2},
				{PubKey: changes[2].PubKey, Power: changes[2].Power + 3},
				{PubKey: changes[3].PubKey, Power: changes[3].Power + 4},
			},
			expTotalPower: changesPower + 10,
			expValsNum:    len(ccVals) + 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			consumerKeeper.ApplyCCValidatorChanges(ctx, tc.changes)
			gotVals := getCCVals()

			require.Len(t, gotVals, tc.expValsNum)
			require.Equal(t, tc.expTotalPower, sumCCValsPow(gotVals))
		})
	}
}

// TestIsValidatorJailed tests the IsValidatorJailed method for a consumer keeper
func TestIsValidatorJailed(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	// IsValidatorJailed should return false for an arbitrary consensus address
	// (slash functionality removed, always returns false)
	consAddr := []byte{0x01, 0x02, 0x03}
	isJailed, err := consumerKeeper.IsValidatorJailed(ctx, consAddr)
	require.NoError(t, err)
	require.False(t, isJailed)

	// Standalone changeover functionality removed
}

func TestSlash(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	// Slash functionality removed - SlashWithInfractionReason is now a no-op
	// that logs but doesn't send slash packets
	slashed, err := consumerKeeper.SlashWithInfractionReason(ctx, []byte{0x01, 0x02, 0x03}, 5, 6, math.LegacyNewDec(9.0), stakingtypes.Infraction_INFRACTION_DOWNTIME)
	require.NoError(t, err)
	require.True(t, slashed.IsZero()) // Returns zero since no actual slashing happens

	// Standalone changeover functionality removed
}

// Tests the getter and setter behavior for historical info
func TestHistoricalInfo(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()
	ctx = ctx.WithBlockHeight(15)

	// Generate test validators, save them to store, and retrieve stored records
	validators := GenerateValidators(t)
	SetCCValidators(t, consumerKeeper, ctx, validators)
	ccValidators := consumerKeeper.GetAllCCValidator(ctx)
	require.Len(t, ccValidators, len(validators))

	// iterate over validators and convert them to staking type
	sVals := []stakingtypes.Validator{}
	for _, v := range ccValidators {
		pk, err := v.ConsPubKey()
		require.NoError(t, err)

		val, err := stakingtypes.NewValidator("", pk, stakingtypes.Description{})
		require.NoError(t, err)

		// set voting power to random value
		val.Tokens = sdk.TokensFromConsensusPower(tmrand.NewRand().Int64(), sdk.DefaultPowerReduction)
		sVals = append(sVals, val)
	}

	currentHeight := ctx.BlockHeight()

	validatorsWithCodec := stakingtypes.Validators{
		Validators:     sVals,
		ValidatorCodec: consumerKeeper.ValidatorAddressCodec(),
	}
	// create and store historical info
	hi := stakingtypes.NewHistoricalInfo(ctx.BlockHeader(), validatorsWithCodec, sdk.DefaultPowerReduction)
	consumerKeeper.SetHistoricalInfo(ctx, currentHeight, &hi)

	// expect to get historical info
	recv, err := consumerKeeper.GetHistoricalInfo(ctx, currentHeight)
	require.NoError(t, err, "HistoricalInfo not found after set")
	require.Equal(t, hi, recv, "HistoricalInfo not equal")

	// verify that historical info valset has validators sorted in order
	require.True(t, IsValSetSorted(recv.Valset, sdk.DefaultPowerReduction), "HistoricalInfo validators is not sorted")
}

// IsValSetSorted reports whether valset is sorted.
func IsValSetSorted(data []stakingtypes.Validator, powerReduction math.Int) bool {
	n := len(data)
	for i := n - 1; i > 0; i-- {
		if stakingtypes.ValidatorsByVotingPower(data).Less(i, i-1, powerReduction) {
			return false
		}
	}
	return true
}

// Generates 4 test validators with non zero voting power
func GenerateValidators(tb testing.TB) []*tmtypes.Validator {
	tb.Helper()
	numValidators := 4
	validators := []*tmtypes.Validator{}
	for i := 0; i < numValidators; i++ {
		cId := crypto.NewCryptoIdentityFromIntSeed(234 + i)
		pubKey := cId.TMCryptoPubKey()

		votingPower := int64(i + 1)
		validator := tmtypes.NewValidator(pubKey, votingPower)
		validators = append(validators, validator)
	}
	return validators
}

// Sets each input tmtypes.Validator as a types.CrossChainValidator in the consumer keeper store
func SetCCValidators(tb testing.TB, consumerKeeper keeper.Keeper,
	ctx sdk.Context, validators []*tmtypes.Validator,
) {
	tb.Helper()
	for _, v := range validators {
		publicKey, err := cryptocodec.FromCmtPubKeyInterface(v.PubKey)
		require.NoError(tb, err)

		ccv, err := types.NewCCValidator(v.Address, v.VotingPower, publicKey)
		require.NoError(tb, err)
		consumerKeeper.SetCCValidator(ctx, ccv)
	}
}
