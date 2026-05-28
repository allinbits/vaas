package types

import (
	"context"
	"time"

	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	"cosmossdk.io/math"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
)

func DefaultConsumerInitializationParameters() ConsumerInitializationParameters {
	return ConsumerInitializationParameters{
		InitialHeight: clienttypes.Height{
			RevisionNumber: 1,
			RevisionHeight: 1,
		},
		GenesisHash:       []byte{},
		BinaryHash:        []byte{},
		SpawnTime:         time.Time{},
		UnbondingPeriod:   vaastypes.DefaultConsumerUnbondingPeriod,
		VaasTimeoutPeriod: vaastypes.DefaultVAASTimeoutPeriod,
		HistoricalEntries: vaastypes.DefaultHistoricalEntries,
	}
}

func DefaultConsumerInfractionParameters(ctx context.Context, slashingKeeper vaastypes.SlashingKeeper) (InfractionParameters, error) {
	doubleSignSlashingFraction, err := slashingKeeper.SlashFractionDoubleSign(ctx)
	if err != nil {
		return InfractionParameters{}, err
	}

	return InfractionParameters{
		DoubleSign: &SlashJailParameters{
			JailDuration:  time.Duration(1<<63 - 1),
			SlashFraction: doubleSignSlashingFraction,
			Tombstone:     true,
		},
	Downtime: &SlashJailParameters{
			JailDuration:  0,
			SlashFraction: math.LegacyNewDecWithPrec(5, 4),
			Tombstone:     false,
		},
	}, nil
}
