package types

import (
	"cosmossdk.io/collections"
)

const (
	// ModuleName defines the VAAS consumer module name
	ModuleName = "vaasconsumer"

	// StoreKey is the store key string for IBC consumer
	StoreKey = ModuleName

	// RouterKey is the message route for IBC consumer
	RouterKey = ModuleName

	// QuerierRoute is the querier route for IBC consumer
	QuerierRoute = ModuleName
)

// Collection key prefixes for use with cosmossdk.io/collections
var (
	PortPrefix                   = collections.NewPrefix(0)
	UnbondingTimePrefix          = collections.NewPrefix(1)
	ProviderClientIDPrefix       = collections.NewPrefix(2)
	PendingChangesPrefix         = collections.NewPrefix(3)
	PreVAASPrefix                = collections.NewPrefix(4)
	InitialValSetPrefix          = collections.NewPrefix(5)
	HistoricalInfoPrefix         = collections.NewPrefix(6)
	HeightValsetUpdateIDPrefix   = collections.NewPrefix(7)
	CrossChainValidatorPrefix    = collections.NewPrefix(8)
	InitGenesisHeightPrefix      = collections.NewPrefix(9)
	PrevStandaloneChainPrefix    = collections.NewPrefix(10)
	ParametersPrefix             = collections.NewPrefix(11)
	HighestValsetUpdateIDPrefix  = collections.NewPrefix(12)
	ConsumerDebtPrefix           = collections.NewPrefix(13)
	PendingEvidencePacketsPrefix = collections.NewPrefix(14)
)
