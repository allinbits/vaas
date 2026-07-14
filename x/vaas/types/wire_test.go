package types_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/allinbits/vaas/x/vaas/types"
)

func TestValidatorSetChangePacketData_IsSnapshotRoundTrip(t *testing.T) {
	pkt := types.NewValidatorSetChangePacketData(nil, 7)
	pkt.IsSnapshot = true

	bz := pkt.GetBytes()
	var got types.ValidatorSetChangePacketData
	require.NoError(t, types.ModuleCdc.UnmarshalJSON(bz, &got))
	require.True(t, got.IsSnapshot)
	require.Equal(t, uint64(7), got.ValsetUpdateId)
}
