package types_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// Tests the validation of consumer params that happens at genesis
func TestValidateParams(t *testing.T) {
	testCases := []struct {
		name    string
		params  vaastypes.ConsumerParams
		expPass bool
	}{
		{"default params", vaastypes.DefaultParams(), true},
		{
			"custom valid params",
			vaastypes.NewParams(true, vaastypes.DefaultVAASTimeoutPeriod, 1000, 24*21*time.Hour), true,
		},
		{
			"custom invalid params, VAAS timeout",
			vaastypes.NewParams(true, 0, 1000, 24*21*time.Hour), false,
		},
		{
			"custom invalid params, negative num historical entries",
			vaastypes.NewParams(true, vaastypes.DefaultVAASTimeoutPeriod, -100, 24*21*time.Hour), false,
		},
		{
			"custom invalid params, negative unbonding period",
			vaastypes.NewParams(true, vaastypes.DefaultVAASTimeoutPeriod, 1000, -24*21*time.Hour), false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.params.Validate()
			if tc.expPass {
				require.Nil(t, err, "expected error to be nil for test case: %s", tc.name)
			} else {
				require.NotNil(t, err, "expected error but got nil for test case: %s", tc.name)
			}
		})
	}
}
