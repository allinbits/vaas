package types

import (
	"errors"
	"fmt"
	"time"

	"cosmossdk.io/math"
)

const (
	// DefaultVAASTimeoutPeriod is the IBC packet timeout for VAAS packets.
	// One epoch-scale value: undelivered packets are superseded the next
	// epoch and late ones are dropped by the consumer, so a long timeout
	// buys nothing. Must be <= MaxTimeoutDelta (24h, ibc-go v2 hard cap).
	DefaultVAASTimeoutPeriod = time.Hour

	// MinVAASTimeoutPeriod is the floor for VaasTimeoutPeriod, comfortably
	// above realistic IBC relay latency so packets are not expired before
	// they can be delivered.
	MinVAASTimeoutPeriod = 10 * time.Minute

	// MinConsumerUnbondingPeriod floors the consumer unbonding so the
	// relayer-derived trusting period (unbonding * fraction) is at least a
	// few days.
	MinConsumerUnbondingPeriod = 5 * 24 * time.Hour
)

var KeyVAASTimeoutPeriod = []byte("VaasTimeoutPeriod")

func ValidateDuration(d time.Duration) error {
	if d <= time.Duration(0) {
		return errors.New("duration must be positive")
	}
	return nil
}

// ValidateVAASTimeoutPeriod checks the VAAS packet timeout is within
// [MinVAASTimeoutPeriod, maxTimeoutDelta]. maxTimeoutDelta is passed in to
// avoid importing ibc-go from this shared package.
func ValidateVAASTimeoutPeriod(d, maxTimeoutDelta time.Duration) error {
	if d < MinVAASTimeoutPeriod {
		return fmt.Errorf("VAAS timeout period must be >= %s, got %s", MinVAASTimeoutPeriod, d)
	}
	if d > maxTimeoutDelta {
		return fmt.Errorf("VAAS timeout period must be <= %s (MaxTimeoutDelta), got %s", maxTimeoutDelta, d)
	}
	return nil
}

func ValidatePositiveInt64(n int64) error {
	if n <= int64(0) {
		return errors.New("int must be positive")
	}
	return nil
}

func ValidateStringFractionNonZero(str string) error {
	dec, err := math.LegacyNewDecFromStr(str)
	if err != nil {
		return err
	}
	if dec.IsNegative() {
		return fmt.Errorf("param cannot be negative, got %s", str)
	}
	if dec.Sub(math.LegacyNewDec(1)).IsPositive() {
		return fmt.Errorf("param cannot be greater than 1, got %s", str)
	}
	if dec.IsZero() {
		return fmt.Errorf("param cannot be zero, got %s", str)
	}
	return nil
}

func ValidateFraction(dec math.LegacyDec) error {
	if dec.IsNegative() {
		return fmt.Errorf("param cannot be negative, got %s", dec)
	}
	if dec.Sub(math.LegacyNewDec(1)).IsPositive() {
		return fmt.Errorf("param cannot be greater than 1, got %s", dec)
	}
	return nil
}

func CalculateTrustPeriod(unbondingPeriod time.Duration, defaultTrustPeriodFraction string) (time.Duration, error) {
	trustDec, err := math.LegacyNewDecFromStr(defaultTrustPeriodFraction)
	if err != nil {
		return time.Duration(0), err
	}
	trustPeriod := time.Duration(trustDec.MulInt64(unbondingPeriod.Nanoseconds()).TruncateInt64())

	return trustPeriod, nil
}
