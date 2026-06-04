package types

import (
	"errors"
	"fmt"
	"time"

	"cosmossdk.io/math"
)

const (
	// DefaultVAASTimeoutPeriod is 4 weeks to ensure channel doesn't close on timeout.
	DefaultVAASTimeoutPeriod = 4 * 7 * 24 * time.Hour
)

var KeyVAASTimeoutPeriod = []byte("VaasTimeoutPeriod")

func ValidateDuration(d time.Duration) error {
	if d <= time.Duration(0) {
		return errors.New("duration must be positive")
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
