// Package e2e defines the integration (end-to-end) test suite for the
// VAAS (Validation as a Service) provider-consumer chain setup.
//
// It uses Docker containers (via dockertest) to run a provider chain,
// a consumer chain, and the ibc-v2-ts-relayer, validating the full CCV
// (Cross-Chain Validation) lifecycle including:
//
//   - Provider chain initialization and block production
//   - Consumer chain registration on the provider
//   - Consumer genesis bootstrapping from provider state
//   - IBC v2 counterparty registration (via ts-relayer)
//   - Validator set synchronization via VSC packets (relayed by ts-relayer)
//
// The ts-relayer is available at https://github.com/allinbits/ibc-v2-ts-relayer
package e2e
