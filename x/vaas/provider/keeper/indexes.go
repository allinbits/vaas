package keeper

import (
	"cosmossdk.io/collections"
	"cosmossdk.io/collections/indexes"
)

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
