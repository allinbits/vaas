package types_test

import (
	"encoding/json"
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
		{"custom valid params", types.NewParams("0.33", "0.5", time.Hour, 1000, math.NewInt(42), types.DefaultMinDepositBlocks, types.DefaultMaxPauseDuration), true},
		{"zero fees per block", types.NewParams("0.33", "0.5", time.Hour, 1000, math.NewInt(0), types.DefaultMinDepositBlocks, types.DefaultMaxPauseDuration), false},
		{"0 trusting period fraction", types.NewParams(
			"0.00", "0.5", time.Hour, 1000, math.NewInt(1), types.DefaultMinDepositBlocks, types.DefaultMaxPauseDuration), false},
		{"0 liveness grace fraction", types.NewParams(
			"0.33", "0.00", time.Hour, 1000, math.NewInt(1), types.DefaultMinDepositBlocks, types.DefaultMaxPauseDuration), false},
		{"0 ccv timeout period", types.NewParams(
			"0.33", "0.5", 0, 1000, math.NewInt(1), types.DefaultMinDepositBlocks, types.DefaultMaxPauseDuration), false},
		{"0 max pause duration", types.NewParams(
			"0.33", "0.5", time.Hour, 1000, math.NewInt(1), types.DefaultMinDepositBlocks, 0), false},
		{"negative max pause duration", types.NewParams(
			"0.33", "0.5", time.Hour, 1000, math.NewInt(1), types.DefaultMinDepositBlocks, -time.Hour), false},
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

	// maxMissed = W - ceil(minSigned*W) = 600 - 300 = 300
	require.Equal(t, int64(300), ip.MaxMissed())

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
