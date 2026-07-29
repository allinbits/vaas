# AGENTS.md

## What This Is

VAAS (Validator-as-a-Service) is a simplified Interchain Security (ICS) implementation for Cosmos blockchains, derived from [interchain-security](https://github.com/cosmos/interchain-security). It lets a provider chain lease its proof-of-stake security to consumer chains via automatic validator synchronization. All active validators validate all consumers — no opt-in/opt-out.

VAAS runs on **IBC v2 only** (client-based, out-of-order). There is no IBC v1 channel handshake; the VAAS modules register on `ibcRouterV2` under application IDs `vaasprovider` and `vaasconsumer` (see [x/vaas/types/keys.go](x/vaas/types/keys.go)). Consumer launch relies on a relayer (ts-relayer in localnet and e2e) creating IBC v2 clients on both sides; the provider then discovers its consumer client at the next epoch boundary.

## Build & Test Commands

```bash
make build              # go build ./...
make test               # all unit tests (excludes e2e), 25m timeout
make lint               # golangci-lint via devdeps/go.mod
make lint-fix           # auto-fix lint issues
make mocks-gen          # regenerate mocks from x/vaas/types/expected_keepers.go
make vulncheck          # govulncheck

# Single test
go test ./x/vaas/provider/keeper -run TestConsumerClientId -v

# Protobuf (requires Docker)
make proto-gen          # generate Go code from .proto files
make proto-format       # clang-format
make proto-lint         # buf lint

# Localnet (3 terminals, or use localnet-start for all-in-one)
make localnet-start     # provider + consumer + ts-relayer (Docker)
make localnet-clean     # stop all, clean data

# E2E (Docker-based)
make docker-build-all   # build chain image (ts-relayer image is pulled)
make test-e2e           # run e2e suite
```

Dev tool dependencies are isolated in `devdeps/go.mod` and invoked via `go run -modfile devdeps/go.mod`.

## Architecture

### Module Structure

The core protocol lives in `x/vaas/` with two symmetric modules:

- **`x/vaas/provider/`** — runs on the provider chain. Manages consumer lifecycle, generates VSC (Validator Set Change) packets at epoch boundaries, handles key assignment, processes double-voting evidence.
- **`x/vaas/consumer/`** — runs on consumer chains. Receives VSC packets, maintains cross-chain validator set, reports evidence back to provider.
- **`x/vaas/types/`** — shared types, errors, constants, and `expected_keepers.go` (interfaces for external dependencies like staking/slashing).

Each module has: `keeper/` (business logic + state), `types/` (data structures + params), `client/cli/` (CLI commands), `module.go`, and `ibc_module.go` (IBC v2 callbacks implementing `api.IBCModule` from `ibc-go/v10/modules/core/api`).

Two helper modules replace the standard Cosmos modules on the **provider** app so the staking module does not push its own validator-set updates to CometBFT -- the VAAS provider module computes and returns the provider validator set itself, in `EndBlock`:

- `x/vaas/no_valupdates_staking/` — staking without EndBlock valset exports
- `x/vaas/no_valupdates_genutil/` — genutil without gentx-based valset init

The consumer app wires neither: it runs no real staking module and uses stock `genutil` with the `ConsumerKeeper` standing in as a no-op staking keeper, so a consumer's validator set is driven entirely by incoming VSC packets.

### Application Layer

- **`app/provider/`** — full Cosmos SDK app for the provider chain
- **`app/consumer/`** — full Cosmos SDK app for consumer chains
- **`app/cmd/`** — binary entry points (`provider` and `consumer` daemons)

Built with `make build-apps` into `build/`.

### Key Data Flow

1. Once a consumer reaches `LAUNCHED`, a relayer creates an IBC v2 client on the provider pointing to the consumer (and the counterparty on the other side). The provider discovers this client at the next epoch boundary (`discoverActiveConsumerClient`), it never creates the client itself.
2. Once per epoch (`blocks_per_epoch`, default 600) the provider queues a VSC packet per launched consumer and, in the same window, collects each consumer's epoch fee (`fees_per_block * blocks_per_epoch`) from its fee pool and distributes it to the bonded validators.
3. Provider sends each queued VSC packet over the discovered IBC v2 client. Packets are out-of-order; the consumer deduplicates via `HighestValsetUpdateID`. A packet carries a diff by default, or an absolute snapshot (`is_snapshot`) when the consumer has fallen behind on acks, and every packet also carries the consumer's current in-debt flag.
4. Consumer's `OnRecvPacket` calls `ApplyCCValidatorChanges()` and the new set is flushed to CometBFT on the next `EndBlock`.
5. Evidence flows back to the provider two ways. Downtime evidence travels consumer -> provider as IBC `EvidencePacketData` packets, which the provider prices into a slash held behind a challenge window (a successful `MsgChallengeConsumerDowntime` cancels it and moves the consumer to `PAUSED`). Double-voting / light-client evidence is submitted as ordinary provider transactions (`MsgSubmitConsumerDoubleVoting` / `MsgSubmitConsumerMisbehaviour`). Global infraction parameters determine slash/jail.

### Consumer Lifecycle

`REGISTERED -> INITIALIZED -> LAUNCHED -> STOPPED -> DELETED`, plus a `PAUSED`
branch off `LAUNCHED` (entered by a successful downtime challenge; governance
resumes it to `LAUNCHED` or removes it to `STOPPED`).

Managed on the provider via `MsgCreateConsumer`, `MsgUpdateConsumer`,
`MsgRemoveConsumer`, `MsgChallengeConsumerDowntime`, and `MsgResumeConsumer`;
the per-consumer fee pool via `MsgFundConsumerFeePool`,
`MsgWithdrawConsumerFeePool`, `MsgSweepConsumerFeePool`, and
`MsgSetConsumerFeesPerBlock`.

### State Management

Uses `cosmossdk.io/collections` for type-safe key-value storage. Each keeper defines a `Schema` with indexed maps. Key assignment state maps validator addresses to per-consumer consensus keys (`ValidatorConsumerPubKey`, `ValidatorByConsumerAddr`).

### Proto Definitions

Under `proto/vaas/`: `v1/` (shared wire types like `ValidatorSetChangePacketData`), `provider/v1/`, `consumer/v1/`. Generated Go code goes to corresponding `types/` packages.

## Testing

- **Unit tests**: alongside source in `*_test.go`. Use `testutil/keeper/unit_test_helpers.go` for in-memory keeper setup with `MockedKeepers` (gomock).
- **Mock generation**: `make mocks-gen` from `x/vaas/types/expected_keepers.go` -> `testutil/keeper/mocks.go`
- **E2E tests**: `tests/e2e/` — Docker-based, spins up real provider/consumer chains plus the `ghcr.io/allinbits/ibc-v2-ts-relayer` container; see [tests/e2e/e2e_tsrelayer_test.go](tests/e2e/e2e_tsrelayer_test.go).

## Lint / Import Ordering

Import groups (enforced by gci in `.golangci.yml`):

1. Standard library
2. Third-party
3. `github.com/cometbft/cometbft`
4. `github.com/cosmos/*`, `cosmossdk.io/*`, `github.com/cosmos/cosmos-sdk`
5. `github.com/atomone-hub/atomone`

Generated files (`*.pb.go`, `*.pb.gw.go`) and `tests/` directory are excluded from linting.
