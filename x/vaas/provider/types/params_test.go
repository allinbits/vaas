package types_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"

	"cosmossdk.io/math"

	"github.com/allinbits/vaas/x/vaas/provider/types"
)

func TestValidateParams(t *testing.T) {
	testCases := []struct {
		name    string
		params  types.Params
		expPass bool
	}{
		{"default params", types.DefaultParams(), true},
		{"custom valid params", types.NewParams("0.33", "0.5", time.Hour, 1000, 180, math.NewInt(42), types.DefaultMinDepositBlocks), true},
		{"zero fees per block", types.NewParams("0.33", "0.5", time.Hour, 1000, 180, math.NewInt(0), types.DefaultMinDepositBlocks), false},
		{"0 trusting period fraction", types.NewParams(
			"0.00", "0.5", time.Hour, 1000, 180, math.NewInt(1), types.DefaultMinDepositBlocks), false},
		{"0 liveness grace fraction", types.NewParams(
			"0.33", "0.00", time.Hour, 1000, 180, math.NewInt(1), types.DefaultMinDepositBlocks), false},
		{"0 ccv timeout period", types.NewParams(
			"0.33", "0.5", 0, 1000, 180, math.NewInt(1), types.DefaultMinDepositBlocks), false},
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
			DoubleSign:          types.DefaultInfractionParameters().DoubleSign,
			Downtime:            types.DefaultInfractionParameters().Downtime,
			DowntimeGracePeriod: 0,
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
