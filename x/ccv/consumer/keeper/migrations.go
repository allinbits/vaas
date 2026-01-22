package keeper

import (
	paramtypes "github.com/cosmos/cosmos-sdk/x/params/types"
)

// Migrator is a struct for handling in-place store migrations.
type Migrator struct {
	keeper     Keeper
	paramSpace paramtypes.Subspace
}

// NewMigrator returns a new Migrator.
func NewMigrator(keeper Keeper, paramspace paramtypes.Subspace) Migrator {
	return Migrator{keeper: keeper, paramSpace: paramspace}
}

// Note: Migration functions removed as this is a fresh module start.
// The following migrations would be available in the original module:
// - Migrate1to2
// - Migrate2to3
// - Migrate3to4
