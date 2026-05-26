package keeper

import (
	"cosmossdk.io/collections"
	"cosmossdk.io/collections/indexes"
)

// ConsumerClientIndexes defines the indexes for consumer-client mappings
// Primary key: ConsumerId (uint64) -> Value: ClientId (string)
// Index: ByClientId allows reverse lookup from ClientId -> ConsumerId
type ConsumerClientIndexes struct {
	// ByClientId is a unique index that maps ClientId -> ConsumerId
	ByClientId *indexes.Unique[string, uint64, string]
}

func (i ConsumerClientIndexes) IndexesList() []collections.Index[uint64, string] {
	return []collections.Index[uint64, string]{i.ByClientId}
}
