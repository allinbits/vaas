package types_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"

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

// TestConsumerParamsOwnerAddressValidation: the owner seeded by the provider
// arrives rendered under the provider's bech32 prefix, so validation accepts
// any decodable bech32 (or empty, which leaves the pin to governance alone)
// and rejects only strings no prefix could have produced.
func TestConsumerParamsOwnerAddressValidation(t *testing.T) {
	p := types.DefaultConsumerParams()
	p.OwnerAddress = ""
	require.NoError(t, p.Validate(), "empty owner is valid: governance-only pin")

	p.OwnerAddress = "cosmos1dkas8mu4kyhl5jrh4nzvm65qz588hy9qcz08la"
	require.NoError(t, p.Validate())

	raw, err := sdk.GetFromBech32("cosmos1dkas8mu4kyhl5jrh4nzvm65qz588hy9qcz08la", "cosmos")
	require.NoError(t, err)
	foreign, err := sdk.Bech32ifyAddressBytes("provider", raw)
	require.NoError(t, err)
	p.OwnerAddress = foreign
	require.NoError(t, p.Validate(), "a foreign bech32 prefix is valid: bytes are compared, not strings")

	p.OwnerAddress = "not-bech32-at-all"
	require.Error(t, p.Validate())
}
