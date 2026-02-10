// Package e2e defines the integration (end-to-end) test suite for the
// VAAS (Validating as a Service) provider-consumer chain setup.
//
// It uses Docker containers (via dockertest) to run a provider chain,
// a consumer chain, and a Hermes IBC relayer, validating the full
// CCV (Cross-Chain Validation) lifecycle including:
//
//   - Provider chain initialization and block production
//   - Consumer chain registration on the provider
//   - Consumer genesis bootstrapping from provider state
//   - Validator set synchronization via VSC packets
//   - IBC relaying of VAAS packets via Hermes
package e2e
