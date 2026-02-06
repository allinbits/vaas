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
	PortPrefix                 = collections.NewPrefix(0)
	UnbondingTimePrefix        = collections.NewPrefix(2)
	ProviderClientIDPrefix     = collections.NewPrefix(3)
	ProviderChannelIDPrefix    = collections.NewPrefix(4)
	PendingChangesPrefix       = collections.NewPrefix(5)
	PreVAASPrefix              = collections.NewPrefix(7)
	InitialValSetPrefix        = collections.NewPrefix(8)
	HistoricalInfoPrefix       = collections.NewPrefix(11)
	HeightValsetUpdateIDPrefix = collections.NewPrefix(13)
	CrossChainValidatorPrefix  = collections.NewPrefix(16)
	InitGenesisHeightPrefix    = collections.NewPrefix(17)
	PrevStandaloneChainPrefix  = collections.NewPrefix(19)
	ParametersPrefix           = collections.NewPrefix(22)
)
