package types

import (
	"context"
	"time"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"

	"cosmossdk.io/math"

	vaastypes "github.com/allinbits/vaas/x/vaas/types"
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
	jailDuration, err := slashingKeeper.DowntimeJailDuration(ctx)
	if err != nil {
		return InfractionParameters{}, err
	}

	doubleSignSlashingFraction, err := slashingKeeper.SlashFractionDoubleSign(ctx)
	if err != nil {
		return InfractionParameters{}, err
	}

	return InfractionParameters{
		DoubleSign: &SlashJailParameters{
			JailDuration:  time.Duration(1<<63 - 1), // the largest value a time.Duration can hold 9223372036854775807 (approximately 292 years)
			SlashFraction: doubleSignSlashingFraction,
			Tombstone:     true,
		},
		Downtime: &SlashJailParameters{
			JailDuration:  jailDuration,
			SlashFraction: math.LegacyNewDec(0),
			Tombstone:     false,
		},
	}, nil
}
