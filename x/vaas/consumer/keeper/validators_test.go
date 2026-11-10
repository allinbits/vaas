package keeper_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	tmrand "github.com/cometbft/cometbft/libs/rand"
	tmtypes "github.com/cometbft/cometbft/types"

	"cosmossdk.io/core/comet"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdktestutil "github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	"github.com/cosmos/cosmos-sdk/x/slashing"
	slashingkeeper "github.com/cosmos/cosmos-sdk/x/slashing/keeper"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/allinbits/vaas/testutil/crypto"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	"github.com/allinbits/vaas/x/vaas/consumer/keeper"
	"github.com/allinbits/vaas/x/vaas/consumer/types"
)

// TestApplyVaasValidatorChanges tests the ApplyVaasValidatorChanges method for a consumer keeper
func TestApplyVaasValidatorChanges(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	// utility functions
	getVaasVals := func() (vals []types.VaasValidator) {
		vals = consumerKeeper.GetAllVaasValidator(ctx)
		return
	}

	clearVaasVals := func() {
		vaasVals := consumerKeeper.GetAllVaasValidator(ctx)
		for _, v := range vaasVals {
			consumerKeeper.DeleteVaasValidator(ctx, v.Address)
		}
	}

	sumVaasValsPow := func(vals []types.VaasValidator) (power int64) {
		for _, v := range vals {
			power += v.Power
		}
		return
	}

	// prepare the testing setup by clearing the current VAAS validators in states
	clearVaasVals()

	tcValidators := GenerateValidators(t)

	changes := []abci.ValidatorUpdate{}
	changesPower := int64(0)

	for _, v := range tcValidators {
		changes = append(changes, tmtypes.TM2PB.ValidatorUpdate(v))
		changesPower += v.VotingPower
	}

	// finish setup by storing 3 out 4 testing validators as VAAS validator records
	SetVaasValidators(t, consumerKeeper, ctx, tcValidators[:len(tcValidators)-1])

	// verify setup
	vaasVals := getVaasVals()
	require.Len(t, vaasVals, len(tcValidators)-1)

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
			expValsNum:    len(vaasVals) + 1,
		},
		{
			name:          "update a validator voting power",
			changes:       []abci.ValidatorUpdate{{PubKey: changes[0].PubKey, Power: changes[0].Power + 3}},
			expTotalPower: changesPower + 3,
			expValsNum:    len(vaasVals) + 1,
		},
		{
			name:          "unbond a validator",
			changes:       []abci.ValidatorUpdate{{PubKey: changes[0].PubKey, Power: 0}},
			expTotalPower: changesPower - changes[0].Power,
			expValsNum:    len(vaasVals),
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
			expValsNum:    len(vaasVals) + 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			consumerKeeper.ApplyVaasValidatorChanges(ctx, tc.changes)
			gotVals := getVaasVals()

			require.Len(t, gotVals, tc.expValsNum)
			require.Equal(t, tc.expTotalPower, sumVaasValsPow(gotVals))
		})
	}
}

// TestValidatorByConsAddr pins the stand-in x/slashing sees on a consumer:
// every validator reads as jailed, through ValidatorByConsAddr and
// IsValidatorJailed alike, and the stub answers GetConsAddr with the queried
// address itself, since the consumer runs no consensus-key rotation. A zero
// staking validator instead panics inside GetConsAddr (nil consensus pubkey
// Any).
func TestValidatorByConsAddr(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consAddr := sdk.ConsAddress([]byte{0x01, 0x02, 0x03})
	val, err := consumerKeeper.ValidatorByConsAddr(ctx, consAddr)
	require.NoError(t, err)
	require.NotNil(t, val)
	require.True(t, val.IsJailed(), "x/slashing leaves a jailed validator's liveness alone")
	isJailed, err := consumerKeeper.IsValidatorJailed(ctx, consAddr)
	require.NoError(t, err)
	require.True(t, isJailed)

	got, err := val.GetConsAddr()
	require.NoError(t, err)
	require.Equal(t, consAddr.Bytes(), got, "the rotation remap must be the identity on a consumer")
}

// TestSlashingLivenessTrackingIsInertOnTheConsumer drives x/slashing's
// per-vote HandleValidatorSignature with the consumer keeper standing in as
// its staking keeper, as the consumer app wires it, and pins that absent
// votes leave no trace there: no liveness or slash event, no missed-block
// counter, no bitmap bit. Downtime detection lives in the consumer's own
// tumbling windows (downtime.go), under the VAAS downtime parameters.
func TestSlashingLivenessTrackingIsInertOnTheConsumer(t *testing.T) {
	consumerKeeper, _, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	key := storetypes.NewKVStoreKey(slashingtypes.StoreKey)
	ctx := sdktestutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test")).Ctx.WithBlockHeight(50)
	encCfg := moduletestutil.MakeTestEncodingConfig(slashing.AppModuleBasic{})
	slashingKeeper := slashingkeeper.NewKeeper(encCfg.Codec, encCfg.Amino, runtime.NewKVStoreService(key), consumerKeeper, "")
	params := slashingtypes.DefaultParams()
	params.SignedBlocksWindow = 4
	require.NoError(t, slashingKeeper.SetParams(ctx, params))

	consAddr := sdk.ConsAddress([]byte("consumer-validator-1"))
	require.NoError(t, slashingKeeper.SetValidatorSigningInfo(ctx, consAddr,
		slashingtypes.NewValidatorSigningInfo(consAddr, 1, 0, time.Unix(0, 0).UTC(), false, 0)))

	// Well past the window with every vote absent: x/slashing on its own
	// would have counted, flagged, and slashed by now.
	for range 3 * params.SignedBlocksWindow {
		require.NoError(t, slashingKeeper.HandleValidatorSignature(ctx, consAddr.Bytes(), 10, comet.BlockIDFlagAbsent))
	}

	info, err := slashingKeeper.GetValidatorSigningInfo(ctx, consAddr)
	require.NoError(t, err)
	require.Zero(t, info.MissedBlocksCounter)
	require.Zero(t, info.IndexOffset)
	require.True(t, info.JailedUntil.IsZero() || info.JailedUntil.Equal(time.Unix(0, 0).UTC()))
	for i := range params.SignedBlocksWindow {
		missed, err := slashingKeeper.GetMissedBlockBitmapValue(ctx, consAddr, i)
		require.NoError(t, err)
		require.False(t, missed)
	}
	for _, ev := range ctx.EventManager().Events() {
		require.NotEqual(t, slashingtypes.EventTypeLiveness, ev.Type)
		require.NotEqual(t, slashingtypes.EventTypeSlash, ev.Type)
	}
}

// TestSlash asserts that SlashWithInfractionReason never queues an evidence
// packet, for any infraction type. VAAS-owned tumbling-window tracking
// (downtime.go) is the sole source of downtime evidence; x/slashing calls
// into this method are informational only.
func TestSlash(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	addr := sdk.ConsAddress([]byte{0x01, 0x02, 0x03})

	slashed, err := consumerKeeper.SlashWithInfractionReason(ctx, addr, 5, 6, math.LegacyNewDec(9.0), stakingtypes.Infraction_INFRACTION_DOWNTIME)
	require.NoError(t, err)
	require.True(t, slashed.IsZero())
	require.Equal(t, 0, consumerKeeper.GetPendingEvidencePacketCount(ctx))

	// double-sign should not queue a packet either
	slashed, err = consumerKeeper.SlashWithInfractionReason(ctx, addr, 5, 6, math.LegacyNewDec(9.0), stakingtypes.Infraction_INFRACTION_DOUBLE_SIGN)
	require.NoError(t, err)
	require.True(t, slashed.IsZero())
	require.Equal(t, 0, consumerKeeper.GetPendingEvidencePacketCount(ctx))
}

// TestSlashRepeatedDowntimeQueuesNothing asserts that repeated downtime
// slash requests for the same or different validators never queue evidence
// packets, since detection and queueing live exclusively in downtime.go.
func TestSlashRepeatedDowntimeQueuesNothing(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	addr := sdk.ConsAddress([]byte{0x01, 0x02, 0x03})

	slashed, err := consumerKeeper.SlashWithInfractionReason(ctx, addr, 5, 6, math.LegacyNewDec(9.0), stakingtypes.Infraction_INFRACTION_DOWNTIME)
	require.NoError(t, err)
	require.True(t, slashed.IsZero())
	require.Equal(t, 0, consumerKeeper.GetPendingEvidencePacketCount(ctx))

	slashed, err = consumerKeeper.SlashWithInfractionReason(ctx, addr, 5, 6, math.LegacyNewDec(9.0), stakingtypes.Infraction_INFRACTION_DOWNTIME)
	require.NoError(t, err)
	require.True(t, slashed.IsZero())
	require.Equal(t, 0, consumerKeeper.GetPendingEvidencePacketCount(ctx))

	addr2 := sdk.ConsAddress([]byte{0x04, 0x05, 0x06})
	slashed, err = consumerKeeper.SlashWithInfractionReason(ctx, addr2, 5, 6, math.LegacyNewDec(9.0), stakingtypes.Infraction_INFRACTION_DOWNTIME)
	require.NoError(t, err)
	require.True(t, slashed.IsZero())
	require.Equal(t, 0, consumerKeeper.GetPendingEvidencePacketCount(ctx))
}

// Tests the getter and setter behavior for historical info
func TestHistoricalInfo(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()
	ctx = ctx.WithBlockHeight(15)

	// Generate test validators, save them to store, and retrieve stored records
	validators := GenerateValidators(t)
	SetVaasValidators(t, consumerKeeper, ctx, validators)
	vaasValidators := consumerKeeper.GetAllVaasValidator(ctx)
	require.Len(t, vaasValidators, len(validators))

	// iterate over validators and convert them to staking type
	sVals := []stakingtypes.Validator{}
	for _, v := range vaasValidators {
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
	for i := range numValidators {
		cId := crypto.NewCryptoIdentityFromIntSeed(234 + i)
		pubKey := cId.TMCryptoPubKey()

		votingPower := int64(i + 1)
		validator := tmtypes.NewValidator(pubKey, votingPower)
		validators = append(validators, validator)
	}
	return validators
}

// Sets each input tmtypes.Validator as a types.VaasValidator in the consumer keeper store
func SetVaasValidators(tb testing.TB, consumerKeeper keeper.Keeper,
	ctx sdk.Context, validators []*tmtypes.Validator,
) {
	tb.Helper()
	for _, v := range validators {
		publicKey, err := cryptocodec.FromCmtPubKeyInterface(v.PubKey)
		require.NoError(tb, err)

		vaasVal, err := types.NewVaasValidator(v.Address, v.VotingPower, publicKey)
		require.NoError(tb, err)
		consumerKeeper.SetVaasValidator(ctx, vaasVal)
	}
}
