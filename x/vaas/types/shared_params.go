package types

import (
	"errors"
	"fmt"
	"time"

	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"

	"cosmossdk.io/math"
)

const (
	// DefaultVAASTimeoutPeriod equals the IBC v2 MaxTimeoutDelta (24h), the hard
	// cap IBC applies to every packet's timeout. A larger value is rejected by
	// ValidateVAASTimeoutPeriod (and would otherwise be silently clamped on send).
	DefaultVAASTimeoutPeriod = channeltypesv2.MaxTimeoutDelta
)

var KeyVAASTimeoutPeriod = []byte("VaasTimeoutPeriod")

func ValidateDuration(d time.Duration) error {
	if d <= time.Duration(0) {
		return errors.New("duration must be positive")
	}
	return nil
}

// ValidateVAASTimeoutPeriod validates a VAAS IBC packet timeout: it must be
// positive and must not exceed the IBC v2 MaxTimeoutDelta, since any larger value
// is silently clamped to MaxTimeoutDelta when packets are sent.
func ValidateVAASTimeoutPeriod(d time.Duration) error {
	if err := ValidateDuration(d); err != nil {
		return err
	}
	if d > channeltypesv2.MaxTimeoutDelta {
		return fmt.Errorf("timeout period %s must not exceed IBC v2 max timeout delta %s", d, channeltypesv2.MaxTimeoutDelta)
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
