// Package bech32prefix reads the bech32 account address prefix the reference
// binaries configure at start-up.
package bech32prefix

import (
	"fmt"
	"os"
	"regexp"
)

const (
	// EnvVar names the environment variable that sets the prefix.
	EnvVar = "VAAS_BECH32_PREFIX"
	// Default is the prefix used when EnvVar is unset.
	Default = "cosmos"
	// maxLen keeps the longest derived prefix, the validator operator public
	// key one (prefix + "valoperpub"), within the 83 characters bech32 allows
	// for a human-readable part.
	maxLen = 83 - len("valoperpub")
)

var valid = regexp.MustCompile(`^[a-z][a-z02-9]*$`)

// FromEnv returns the account address prefix named by VAAS_BECH32_PREFIX, or
// Default when the variable is unset. The validator and consensus prefixes
// derive from it as cosmos-sdk's conventions prescribe. A value that is set
// but empty or malformed (see Validate) is refused, so the binary stops at
// start-up instead of at the first address it encodes or decodes.
func FromEnv() (string, error) {
	prefix, ok := os.LookupEnv(EnvVar)
	if !ok {
		return Default, nil
	}
	if err := Validate(prefix); err != nil {
		return "", fmt.Errorf("%s: %w", EnvVar, err)
	}
	return prefix, nil
}

// Validate returns an error unless prefix is a lowercase letter followed by
// lowercase letters and digits other than 1, the bech32 separator, short
// enough for every prefix derived from it.
func Validate(prefix string) error {
	if prefix == "" {
		return fmt.Errorf("bech32 prefix must not be empty; unset the variable to use %q", Default)
	}
	if len(prefix) > maxLen {
		return fmt.Errorf("bech32 prefix %q is longer than %d characters", prefix, maxLen)
	}
	if !valid.MatchString(prefix) {
		return fmt.Errorf("bech32 prefix %q must be a lowercase letter followed by lowercase letters and digits other than 1, the bech32 separator", prefix)
	}
	return nil
}
