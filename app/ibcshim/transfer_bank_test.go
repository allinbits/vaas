package ibcshim_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	transfertypes "github.com/cosmos/ibc-go/v10/modules/apps/transfer/types"

	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"

	"github.com/allinbits/vaas/app/ibcshim"
)

type recordingBankKeeper struct {
	transfertypes.BankKeeper
	stored []banktypes.Metadata
}

func (r *recordingBankKeeper) SetDenomMetaData(_ context.Context, metadata banktypes.Metadata) {
	r.stored = append(r.stored, metadata)
}

// TestTransferBankKeeperStoresValidVoucherMetadata: the metadata ibc-go
// writes for a received voucher (ibc-go v10.5 SetDenomMetadata, reproduced
// here) fails the bank module's validation, and is stored corrected; valid
// metadata is stored as given.
func TestTransferBankKeeperStoresValidVoucherMetadata(t *testing.T) {
	denom := transfertypes.NewDenom("uphoton", transfertypes.NewHop("transfer", "07-tendermint-0"))
	asIBCGoWritesIt := banktypes.Metadata{
		Description: "IBC token from " + denom.Path(),
		DenomUnits:  []*banktypes.DenomUnit{{Denom: denom.Base, Exponent: 0}},
		Base:        denom.IBCDenom(),
		Display:     denom.Path(),
		Name:        denom.Path() + " IBC token",
		Symbol:      "UPHOTON",
	}
	require.Error(t, asIBCGoWritesIt.Validate(), "the premise: ibc-go's metadata fails the bank validation")

	recorder := &recordingBankKeeper{}
	k := ibcshim.NewTransferBankKeeper(recorder)
	k.SetDenomMetaData(context.Background(), asIBCGoWritesIt)
	require.Len(t, recorder.stored, 1)
	stored := recorder.stored[0]
	require.NoError(t, stored.Validate())
	require.Equal(t, denom.IBCDenom(), stored.Base)
	require.Equal(t, denom.IBCDenom(), stored.Display)
	require.Equal(t, asIBCGoWritesIt.Name, stored.Name, "ibc-go's name is kept")
	require.Equal(t, asIBCGoWritesIt.Symbol, stored.Symbol, "ibc-go's symbol is kept")
	require.Equal(t, asIBCGoWritesIt.Description, stored.Description, "ibc-go's description is kept")

	valid := banktypes.Metadata{
		Description: "the bond denom",
		DenomUnits:  []*banktypes.DenomUnit{{Denom: "uatone", Exponent: 0}, {Denom: "atone", Exponent: 6}},
		Base:        "uatone",
		Display:     "atone",
		Name:        "atone",
		Symbol:      "ATONE",
	}
	k.SetDenomMetaData(context.Background(), valid)
	require.Equal(t, valid, recorder.stored[1], "valid metadata is stored as given")
}
