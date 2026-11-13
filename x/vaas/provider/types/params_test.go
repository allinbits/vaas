package types_test

import (
	"encoding/json"
	stdmath "math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"

	"cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"

	"github.com/allinbits/vaas/x/vaas/provider/types"
)

func TestValidateParams(t *testing.T) {
	testCases := []struct {
		name    string
		params  types.Params
		expPass bool
	}{
		{"default params", types.DefaultParams(), true},
		// 0.6 of the default consumer unbonding period covers the default
		// challengeable interval; a smaller fraction would not (see below).
		{"custom valid params", types.NewParams("0.6", "0.5", time.Hour, 1000, math.NewInt(42), types.DefaultMinDepositBlocks, types.DefaultMaxPauseDuration,
			types.DefaultRefusalPauseThreshold, types.DefaultEquivocationExecutionDelay, types.DefaultRemovalVoteDeferralMargin, types.DefaultInfractionParameters()), true},
		{"zero fees per block", types.NewParams("0.33", "0.5", time.Hour, 1000, math.NewInt(0), types.DefaultMinDepositBlocks, types.DefaultMaxPauseDuration,
			types.DefaultRefusalPauseThreshold, types.DefaultEquivocationExecutionDelay, types.DefaultRemovalVoteDeferralMargin, types.DefaultInfractionParameters()), false},
		{"epoch fee at the int64 bound", types.NewParams("0.6", "0.5", time.Hour, 1000, math.NewInt(stdmath.MaxInt64/1000), types.DefaultMinDepositBlocks, types.DefaultMaxPauseDuration,
			types.DefaultRefusalPauseThreshold, types.DefaultEquivocationExecutionDelay, types.DefaultRemovalVoteDeferralMargin, types.DefaultInfractionParameters()), true},
		{"epoch fee past the int64 bound", types.NewParams("0.6", "0.5", time.Hour, 1000, math.NewInt(stdmath.MaxInt64/1000+1), types.DefaultMinDepositBlocks, types.DefaultMaxPauseDuration,
			types.DefaultRefusalPauseThreshold, types.DefaultEquivocationExecutionDelay, types.DefaultRemovalVoteDeferralMargin, types.DefaultInfractionParameters()), false},
		{"fees per block past int64", types.NewParams("0.6", "0.5", time.Hour, 1, math.NewIntFromUint64(stdmath.MaxUint64), types.DefaultMinDepositBlocks, types.DefaultMaxPauseDuration,
			types.DefaultRefusalPauseThreshold, types.DefaultEquivocationExecutionDelay, types.DefaultRemovalVoteDeferralMargin, types.DefaultInfractionParameters()), false},
		{"0 trusting period fraction", types.NewParams(
			"0.00", "0.5", time.Hour, 1000, math.NewInt(1), types.DefaultMinDepositBlocks, types.DefaultMaxPauseDuration,
			types.DefaultRefusalPauseThreshold, types.DefaultEquivocationExecutionDelay, types.DefaultRemovalVoteDeferralMargin, types.DefaultInfractionParameters(),
		), false},
		{"0 liveness grace fraction", types.NewParams(
			"0.33", "0.00", time.Hour, 1000, math.NewInt(1), types.DefaultMinDepositBlocks, types.DefaultMaxPauseDuration,
			types.DefaultRefusalPauseThreshold, types.DefaultEquivocationExecutionDelay, types.DefaultRemovalVoteDeferralMargin, types.DefaultInfractionParameters(),
		), false},
		{"0 VAAS timeout period", types.NewParams(
			"0.33", "0.5", 0, 1000, math.NewInt(1), types.DefaultMinDepositBlocks, types.DefaultMaxPauseDuration,
			types.DefaultRefusalPauseThreshold, types.DefaultEquivocationExecutionDelay, types.DefaultRemovalVoteDeferralMargin, types.DefaultInfractionParameters(),
		), false},
		{"0 max pause duration", types.NewParams(
			"0.33", "0.5", time.Hour, 1000, math.NewInt(1), types.DefaultMinDepositBlocks, 0,
			types.DefaultRefusalPauseThreshold, types.DefaultEquivocationExecutionDelay, types.DefaultRemovalVoteDeferralMargin, types.DefaultInfractionParameters(),
		), false},
		{"0 refusal pause threshold", types.NewParams(
			"0.6", "0.5", time.Hour, 1000, math.NewInt(1), types.DefaultMinDepositBlocks, types.DefaultMaxPauseDuration,
			"0.00", types.DefaultEquivocationExecutionDelay, types.DefaultRemovalVoteDeferralMargin, types.DefaultInfractionParameters(),
		), false},
		{"refusal pause threshold of one", types.NewParams(
			"0.6", "0.5", time.Hour, 1000, math.NewInt(1), types.DefaultMinDepositBlocks, types.DefaultMaxPauseDuration,
			"1.00", types.DefaultEquivocationExecutionDelay, types.DefaultRemovalVoteDeferralMargin, types.DefaultInfractionParameters(),
		), false},
		{"zero equivocation execution delay", types.NewParams(
			"0.6", "0.5", time.Hour, 1000, math.NewInt(1), types.DefaultMinDepositBlocks, types.DefaultMaxPauseDuration,
			types.DefaultRefusalPauseThreshold, 0, types.DefaultRemovalVoteDeferralMargin, types.DefaultInfractionParameters(),
		), false},
		{"negative removal vote deferral margin", types.NewParams(
			"0.6", "0.5", time.Hour, 1000, math.NewInt(1), types.DefaultMinDepositBlocks, types.DefaultMaxPauseDuration,
			types.DefaultRefusalPauseThreshold, types.DefaultEquivocationExecutionDelay, -time.Minute, types.DefaultInfractionParameters(),
		), false},
		{"invalid infraction parameters", withInfraction(func(ip *types.InfractionParameters) { ip.DoubleSign = nil }), false},
		{"infraction horizon at the trusting period", withInfraction(func(ip *types.InfractionParameters) {
			// 120h + 120h against 0.5 * 480h: the strict inequality rejects it.
			ip.DowntimeChallengeWindow = 120 * time.Hour
			ip.DowntimeEvidenceMaxAge = 120 * time.Hour
		}, "0.5"), false},
		{"negative max pause duration", types.NewParams(
			"0.33", "0.5", time.Hour, 1000, math.NewInt(1), types.DefaultMinDepositBlocks, -time.Hour,
			types.DefaultRefusalPauseThreshold, types.DefaultEquivocationExecutionDelay, types.DefaultRemovalVoteDeferralMargin, types.DefaultInfractionParameters(),
		), false},
	}

	for _, tc := range testCases {
		err := tc.params.Validate()
		if tc.expPass {
			require.Nil(t, err, "expected error to be nil for testcase: %s", tc.name)
		} else {
			require.NotNil(t, err, "expected error but got nil for testcase: %s", tc.name)
		}
	}
}

func TestValidateFeesPerEpoch(t *testing.T) {
	cases := []struct {
		name           string
		feesPerBlock   math.Int
		blocksPerEpoch int64
		expPass        bool
	}{
		{"defaults", math.NewInt(types.DefaultFeesPerBlockAmount), types.DefaultBlocksPerEpoch, true},
		{"product at the bound", math.NewInt(stdmath.MaxInt64 / 600), 600, true},
		{"product past the bound", math.NewInt(stdmath.MaxInt64/600 + 1), 600, false},
		{"max int64 over one block", math.NewInt(stdmath.MaxInt64), 1, true},
		{"amount past int64", math.NewIntFromUint64(stdmath.MaxUint64), 1, false},
		{"nil amount", math.Int{}, 1, false},
	}
	for _, tc := range cases {
		err := types.ValidateFeesPerEpoch(tc.feesPerBlock, tc.blocksPerEpoch)
		if tc.expPass {
			require.NoError(t, err, tc.name)
		} else {
			require.Error(t, err, tc.name)
		}
	}
}

func TestParamsRejectsTimeoutAboveMaxDelta(t *testing.T) {
	p := types.DefaultParams()
	p.VaasTimeoutPeriod = channeltypesv2.MaxTimeoutDelta + time.Hour
	require.Error(t, p.Validate())
}

func TestParamsAcceptsTimeoutAtCap(t *testing.T) {
	p := types.DefaultParams()
	p.VaasTimeoutPeriod = channeltypesv2.MaxTimeoutDelta
	require.NoError(t, p.Validate())
}

func TestValidateInfractionParameters(t *testing.T) {
	testCases := []struct {
		name    string
		params  types.InfractionParameters
		expPass bool
	}{
		{"default infraction params", types.DefaultInfractionParameters(), true},
		{"negative grace period", types.InfractionParameters{
			DoubleSign:          types.DefaultInfractionParameters().DoubleSign,
			Downtime:            types.DefaultInfractionParameters().Downtime,
			DowntimeGracePeriod: -1 * time.Second,
		}, false},
		{"zero grace period (disabled)", types.InfractionParameters{
			DoubleSign:              types.DefaultInfractionParameters().DoubleSign,
			Downtime:                types.DefaultInfractionParameters().Downtime,
			DowntimeGracePeriod:     0,
			SignedBlocksWindow:      types.DefaultInfractionParameters().SignedBlocksWindow,
			MinSignedPerWindow:      types.DefaultInfractionParameters().MinSignedPerWindow,
			DowntimeChallengeWindow: types.DefaultInfractionParameters().DowntimeChallengeWindow,
			DowntimeEvidenceMaxAge:  types.DefaultInfractionParameters().DowntimeEvidenceMaxAge,
		}, true},
		{"nil double_sign", types.InfractionParameters{
			Downtime: types.DefaultInfractionParameters().Downtime,
		}, false},
		{"nil downtime", types.InfractionParameters{
			DoubleSign: types.DefaultInfractionParameters().DoubleSign,
		}, false},
	}

	for _, tc := range testCases {
		err := tc.params.Validate()
		if tc.expPass {
			require.Nil(t, err, "expected error to be nil for testcase: %s", tc.name)
		} else {
			require.NotNil(t, err, "expected error but got nil for testcase: %s", tc.name)
		}
	}
}

func TestInfractionParametersDowntimeWindowValidation(t *testing.T) {
	ip := types.DefaultInfractionParameters()
	require.NoError(t, ip.Validate())
	require.Equal(t, math.LegacyMustNewDecFromStr("0.0001"), ip.Downtime.SlashFraction)

	bad := types.DefaultInfractionParameters()
	bad.SignedBlocksWindow = 0
	require.Error(t, bad.Validate())

	bad = types.DefaultInfractionParameters()
	bad.MinSignedPerWindow = math.LegacyNewDec(2)
	require.Error(t, bad.Validate())

	// challenge window + evidence max age must stay under the trusting period
	bad = types.DefaultInfractionParameters()
	bad.DowntimeChallengeWindow = 30 * 24 * time.Hour
	require.Error(t, types.ValidateInfractionParamsAgainst(bad, types.DefaultTrustingPeriodFraction))
}

// TestInfractionParametersRejectsOmittedSlashFraction verifies that an
// InfractionParameters JSON payload whose downtime object omits the
// slash_fraction key -- which deserializes to a nil LegacyDec, since the
// JSON unmarshal does no defaulting -- fails validation with an error
// instead of booting a chain whose downtime slashes are capped at zero.
func TestInfractionParametersRejectsOmittedSlashFraction(t *testing.T) {
	cdc := codec.NewProtoCodec(codectypes.NewInterfaceRegistry())

	def := types.DefaultInfractionParameters()
	bz, err := cdc.MarshalJSON(&def)
	require.NoError(t, err)

	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(bz, &fields))
	var downtime map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(fields["downtime"], &downtime))
	delete(downtime, "slash_fraction")
	fields["downtime"], err = json.Marshal(downtime)
	require.NoError(t, err)
	bz, err = json.Marshal(fields)
	require.NoError(t, err)

	var ip types.InfractionParameters
	require.NoError(t, cdc.UnmarshalJSON(bz, &ip))
	require.True(t, ip.Downtime.SlashFraction.IsNil())

	var vErr error
	require.NotPanics(t, func() { vErr = ip.Validate() })
	require.Error(t, vErr)
	require.Contains(t, vErr.Error(), "slash_fraction")
}

// TestInfractionParametersRejectsEvidenceMaxAgeAboveChallengeWindow verifies
// that downtime_evidence_max_age can never exceed downtime_challenge_window:
// otherwise, once a pending slash matures and executes, evidence for the
// same (now-closed) window could still be re-accepted -- since the evidence
// is only judged too stale relative to DowntimeEvidenceMaxAge -- and queue a
// second slash for a window already accepted.
func TestInfractionParametersRejectsEvidenceMaxAgeAboveChallengeWindow(t *testing.T) {
	ip := types.DefaultInfractionParameters()
	ip.DowntimeChallengeWindow = 10 * time.Second
	ip.DowntimeEvidenceMaxAge = 10 * time.Second
	require.NoError(t, ip.Validate(), "equal challenge window and evidence max age must be accepted")

	ip.DowntimeEvidenceMaxAge = 10*time.Second + time.Nanosecond
	require.Error(t, ip.Validate(), "evidence max age above challenge window must be rejected")
}

// TestInfractionParametersRejectsZeroDoubleSignSlashFraction verifies that
// equivocation must carry a non-zero stake penalty, while downtime may be
// configured to jail without slashing.
//
// A nil slash_fraction is already refused, but zero passes the shared fraction
// check: it is inside [0,1]. For downtime that is a legitimate policy -- jail
// the validator, take nothing -- and a chain may well want it. For double
// signing it silently removes the entire economic deterrent while the
// parameters still read as configured, which is not a policy anyone selects on
// purpose by leaving a field at zero.
func TestInfractionParametersRejectsZeroDoubleSignSlashFraction(t *testing.T) {
	ip := types.DefaultInfractionParameters()
	ip.DoubleSign.SlashFraction = math.LegacyZeroDec()

	err := ip.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "double_sign")
	require.Contains(t, err.Error(), "slash_fraction")
}

// TestInfractionParametersAllowsZeroDowntimeSlashFraction pins the other half
// of that asymmetry: a jail-only downtime policy is valid configuration.
func TestInfractionParametersAllowsZeroDowntimeSlashFraction(t *testing.T) {
	ip := types.DefaultInfractionParameters()
	ip.Downtime.SlashFraction = math.LegacyZeroDec()

	require.NoError(t, ip.Validate(),
		"a zero downtime slash fraction is a jail-only policy, not a misconfiguration")
}

// withInfraction returns the default params with the infraction parameters
// changed by mutate, and the trusting period fraction overridden when given.
func withInfraction(mutate func(*types.InfractionParameters), fraction ...string) types.Params {
	p := types.DefaultParams()
	mutate(&p.InfractionParameters)
	if len(fraction) > 0 {
		p.TrustingPeriodFraction = fraction[0]
	}
	return p
}
