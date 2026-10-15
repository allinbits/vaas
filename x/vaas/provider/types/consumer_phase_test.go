package types_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
)

func TestConsumerPhasePrelaunched(t *testing.T) {
	prelaunched := map[providertypes.ConsumerPhase]bool{
		providertypes.CONSUMER_PHASE_UNSPECIFIED: false,
		providertypes.CONSUMER_PHASE_REGISTERED:  true,
		providertypes.CONSUMER_PHASE_INITIALIZED: true,
		providertypes.CONSUMER_PHASE_LAUNCHED:    false,
		providertypes.CONSUMER_PHASE_STOPPED:     false,
		providertypes.CONSUMER_PHASE_DELETED:     false,
		providertypes.CONSUMER_PHASE_PAUSED:      false,
	}
	require.Len(t, prelaunched, len(providertypes.ConsumerPhase_name), "every phase is covered")
	for phase, want := range prelaunched {
		require.Equalf(t, want, phase.Prelaunched(), "phase %s", phase)
	}
}
