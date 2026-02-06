package keeper

import (
	"cosmossdk.io/collections"
	"cosmossdk.io/collections/indexes"
)

// ConsumerChannelIndexes defines the indexes for consumer-channel mappings
// Primary key: ConsumerId (string) -> Value: ChannelId (string)
// Index: ByChannelId allows reverse lookup from ChannelId -> ConsumerId
type ConsumerChannelIndexes struct {
	// ByChannelId is a unique index that maps ChannelId -> ConsumerId
	ByChannelId *indexes.Unique[string, string, string]
}

func (i ConsumerChannelIndexes) IndexesList() []collections.Index[string, string] {
	return []collections.Index[string, string]{i.ByChannelId}
}

// ConsumerClientIndexes defines the indexes for consumer-client mappings
// Primary key: ConsumerId (string) -> Value: ClientId (string)
// Index: ByClientId allows reverse lookup from ClientId -> ConsumerId
type ConsumerClientIndexes struct {
	// ByClientId is a unique index that maps ClientId -> ConsumerId
	ByClientId *indexes.Unique[string, string, string]
}

func (i ConsumerClientIndexes) IndexesList() []collections.Index[string, string] {
	return []collections.Index[string, string]{i.ByClientId}
}
