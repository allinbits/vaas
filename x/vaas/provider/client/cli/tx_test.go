package cli

import (
	"testing"

	"github.com/stretchr/testify/require"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
)

func TestHighestHeightBelow(t *testing.T) {
	bound := clienttypes.NewHeight(1, 100)

	testCases := []struct {
		name       string
		heights    []clienttypes.Height
		wantHeight clienttypes.Height
		wantFound  bool
	}{
		{
			name:      "no heights",
			heights:   nil,
			wantFound: false,
		},
		{
			name: "all heights at or above the bound",
			heights: []clienttypes.Height{
				clienttypes.NewHeight(1, 100),
				clienttypes.NewHeight(1, 250),
			},
			wantFound: false,
		},
		{
			name: "picks the highest height below the bound, in any input order",
			heights: []clienttypes.Height{
				clienttypes.NewHeight(1, 40),
				clienttypes.NewHeight(1, 99),
				clienttypes.NewHeight(1, 7),
				clienttypes.NewHeight(1, 150),
			},
			wantHeight: clienttypes.NewHeight(1, 99),
			wantFound:  true,
		},
		{
			name: "a height equal to the bound does not qualify",
			heights: []clienttypes.Height{
				clienttypes.NewHeight(1, 100),
				clienttypes.NewHeight(1, 60),
			},
			wantHeight: clienttypes.NewHeight(1, 60),
			wantFound:  true,
		},
		{
			name: "heights from other revisions never qualify",
			heights: []clienttypes.Height{
				clienttypes.NewHeight(0, 99),
				clienttypes.NewHeight(2, 1),
			},
			wantFound: false,
		},
		{
			name: "same-revision height wins over a numerically larger one from an older revision",
			heights: []clienttypes.Height{
				clienttypes.NewHeight(0, 5000),
				clienttypes.NewHeight(1, 3),
			},
			wantHeight: clienttypes.NewHeight(1, 3),
			wantFound:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, found := highestHeightBelow(tc.heights, bound)
			require.Equal(t, tc.wantFound, found)
			if tc.wantFound {
				require.Equal(t, tc.wantHeight, got)
			}
		})
	}
}
