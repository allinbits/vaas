package types_test

import (
	"testing"
	"time"

	"github.com/allinbits/vaas/x/vaas/provider/types"
	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func TestValidateParams(t *testing.T) {
	testCases := []struct {
		name    string
		params  types.Params
		expPass bool
	}{
		{"default params", types.DefaultParams(), true},
		{"custom valid params", types.NewParams("0.33", time.Hour, 1000, 180, sdk.NewInt64Coin("uphoton", 42)), true},
		{"zero fees per block", types.NewParams("0.33", time.Hour, 1000, 180, sdk.NewInt64Coin("uphoton", 0)), false},
		{"0 trusting period fraction", types.NewParams(
			"0.00", time.Hour, 1000, 180, sdk.NewInt64Coin("uphoton", 1)), false},
		{"0 ccv timeout period", types.NewParams(
			"0.33", 0, 1000, 180, sdk.NewInt64Coin("uphoton", 1)), false},
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
