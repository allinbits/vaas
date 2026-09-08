package types

import (
	"errors"
	"fmt"
	"time"

	"cosmossdk.io/math"
)

const (
	// DefaultSafeModeThreshold is how long the consumer may go without
	// receiving a VSC packet before its tx admission gate enters safe mode
	// (only ibc.core and gov messages pass). Set well below the provider
	// liveness grace period.
	DefaultSafeModeThreshold = 3 * time.Hour

	// DefaultVAASTimeoutPeriod is the IBC packet timeout for VAAS packets.
	// One epoch-scale value: undelivered packets are superseded the next
	// epoch and late ones are dropped by the consumer, so a long timeout
	// buys nothing. Must be <= MaxTimeoutDelta (24h, ibc-go v2 hard cap).
	DefaultVAASTimeoutPeriod = time.Hour
)

var KeyVAASTimeoutPeriod = []byte("VaasTimeoutPeriod")

func ValidateDuration(d time.Duration) error {
	if d <= time.Duration(0) {
		return errors.New("duration must be positive")
	}
	return nil
}

// ValidateVAASTimeoutPeriod checks the VAAS packet timeout is within
// (0, maxTimeoutDelta]. maxTimeoutDelta is passed in to avoid importing ibc-go
// from this shared package. No lower floor is imposed: an undelivered packet is
// superseded by the next epoch's packet and a late one is dropped by the
// consumer's dedup, and a persistently unreachable consumer is handled by the
// liveness sweep -- so a short timeout is not dangerous, and forbidding one only
// blocks testing the timeout path.
func ValidateVAASTimeoutPeriod(d, maxTimeoutDelta time.Duration) error {
	if d <= 0 {
		return fmt.Errorf("VAAS timeout period must be positive, got %s", d)
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

// ValidateStringFractionNonZero checks that str parses to a decimal strictly
// inside the open interval (0, 1). Its two users -- the IBC trusting-period
// fraction and the liveness grace fraction -- each scale the unbonding period
// into a duration that must stay strictly below it (the trusting period must be
// < unbonding for the light client, and the liveness grace must end inside the
// slashable window), so a fraction of exactly 1 is invalid, not just > 1.
func ValidateStringFractionNonZero(str string) error {
	dec, err := math.LegacyNewDecFromStr(str)
	if err != nil {
		return err
	}
	if dec.IsNegative() {
		return fmt.Errorf("param cannot be negative, got %s", str)
	}
	if dec.GTE(math.LegacyOneDec()) {
		return fmt.Errorf("param must be less than 1, got %s", str)
	}
	if dec.IsZero() {
		return fmt.Errorf("param cannot be zero, got %s", str)
	}
	return nil
}

func ValidateFraction(dec math.LegacyDec) error {
	if dec.IsNil() {
		return fmt.Errorf("param cannot be nil")
	}
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
