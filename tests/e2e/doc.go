// Package e2e defines the integration (end-to-end) test suite for the
// VAAS (Validation as a Service) provider-consumer chain setup.
//
// It uses Docker containers (via dockertest) to run a provider chain
// and a consumer chain, validating the full CCV (Cross-Chain Validation)
// lifecycle including:
//
//   - Provider chain initialization and block production
//   - Consumer chain registration on the provider
//   - Consumer genesis bootstrapping from provider state
//   - Validator set synchronization via VSC packets
//
// TODO: IBC v2 counterparty registration and packet relaying requires the ts-relayer.
// See https://github.com/allinbits/ibc-v2-ts-relayer
package e2e
