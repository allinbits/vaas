package types_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	ccvtypes "github.com/allinbits/vaas/x/vaas/types"
)

// Tests the validation of consumer params that happens at genesis
func TestValidateParams(t *testing.T) {
	testCases := []struct {
		name    string
		params  ccvtypes.ConsumerParams
		expPass bool
	}{
		{"default params", ccvtypes.DefaultParams(), true},
		{
			"custom valid params",
			ccvtypes.NewParams(true, ccvtypes.DefaultCCVTimeoutPeriod, 1000, 24*21*time.Hour), true,
		},
		{
			"custom invalid params, ccv timeout",
			ccvtypes.NewParams(true, 0, 1000, 24*21*time.Hour), false,
		},
		{
			"custom invalid params, negative num historical entries",
			ccvtypes.NewParams(true, ccvtypes.DefaultCCVTimeoutPeriod, -100, 24*21*time.Hour), false,
		},
		{
			"custom invalid params, negative unbonding period",
			ccvtypes.NewParams(true, ccvtypes.DefaultCCVTimeoutPeriod, 1000, -24*21*time.Hour), false,
		},
	}

	for _, tc := range testCases {
		err := tc.params.Validate()
		if tc.expPass {
			require.Nil(t, err, "expected error to be nil for test case: %s", tc.name)
		} else {
			require.NotNil(t, err, "expected error but got nil for test case: %s", tc.name)
		}
	}
}
