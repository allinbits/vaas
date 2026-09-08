package app

import (
	"context"

	"cosmossdk.io/math"
)

// fixedRatePhotonKeeper is a stand-in for x/photon's conversion-rate keeper.
// The standalone provider app in this repo has no photon module to wire, so
// it reports a fixed rate instead; AtomOne (the embedding application) wires
// the real x/photon keeper in its place.
type fixedRatePhotonKeeper struct{ rate math.LegacyDec }

// ConversionRate returns the fixed photon-per-bond-token rate configured at
// construction.
func (f fixedRatePhotonKeeper) ConversionRate(context.Context) (math.LegacyDec, error) {
	return f.rate, nil
}
