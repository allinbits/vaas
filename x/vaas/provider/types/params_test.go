package types_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/allinbits/vaas/x/vaas/provider/types"
)

func TestValidateParams(t *testing.T) {
	testCases := []struct {
		name    string
		params  types.Params
		expPass bool
	}{
		{"default params", types.DefaultParams(), true},
		{"custom valid params", types.NewParams(
			"0.33", time.Hour, 1000, 180), true},
		{"0 trusting period fraction", types.NewParams(
			"0.00", time.Hour, 1000, 180), false},
		{"0 ccv timeout period", types.NewParams(
			"0.33", 0, 1000, 180), false},
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
