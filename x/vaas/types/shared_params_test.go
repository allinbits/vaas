package types_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/math"

	"github.com/allinbits/vaas/x/vaas/types"
)

func TestValidateFraction(t *testing.T) {
	testCases := []struct {
		name    string
		dec     math.LegacyDec
		expPass bool
	}{
		{"nil dec", math.LegacyDec{}, false},
		{"negative", math.LegacyMustNewDecFromStr("-0.1"), false},
		{"greater than one", math.LegacyMustNewDecFromStr("1.1"), false},
		{"zero", math.LegacyZeroDec(), true},
		{"one half", math.LegacyMustNewDecFromStr("0.5"), true},
		{"one", math.LegacyOneDec(), true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			require.NotPanics(t, func() { err = types.ValidateFraction(tc.dec) })
			if tc.expPass {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
