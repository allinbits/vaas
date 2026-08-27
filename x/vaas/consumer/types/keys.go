package types

import (
	"cosmossdk.io/collections"
)

const (
	// ModuleName defines the VAAS consumer module name
	ModuleName = "vaasconsumer"

	// StoreKey is the store key string for IBC consumer
	StoreKey = ModuleName
)

// Collection key prefixes for use with cosmossdk.io/collections
var (
	ProviderClientIDPrefix       = collections.NewPrefix(2)
	PendingChangesPrefix         = collections.NewPrefix(3)
	HistoricalInfoPrefix         = collections.NewPrefix(6)
	HeightValsetUpdateIDPrefix   = collections.NewPrefix(7)
	VaasValidatorPrefix          = collections.NewPrefix(8)
	InitGenesisHeightPrefix      = collections.NewPrefix(9)
	ParametersPrefix             = collections.NewPrefix(11)
	HighestValsetUpdateIDPrefix  = collections.NewPrefix(12)
	ConsumerDebtPrefix           = collections.NewPrefix(13)
	PendingEvidencePacketsPrefix = collections.NewPrefix(14)
	LastVSCRecvTimePrefix        = collections.NewPrefix(15)
	MissedBlockBitmapsPrefix     = collections.NewPrefix(16)
	FirstTrackedHeightsPrefix    = collections.NewPrefix(17)
	StagedDowntimeParamsPrefix   = collections.NewPrefix(18)
	ProviderChainIdPrefix        = collections.NewPrefix(19)
)
