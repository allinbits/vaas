package types

import "time"

// DefaultSafeModeThreshold is how long the consumer may go without receiving a
// VSC packet before its tx admission gate enters safe mode (only ibc.core and
// gov messages pass). Set well below the provider liveness grace period.
const DefaultSafeModeThreshold = 3 * time.Hour
