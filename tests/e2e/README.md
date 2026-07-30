# VAAS end-to-end tests

Docker-based integration tests that spin up a real provider chain, a real
consumer chain, and the `ibc-v2-ts-relayer`, then exercise the full VAAS
lifecycle: consumer registration, genesis bootstrapping, IBC v2 client
discovery, VSC synchronization, downtime slashing and challenges, fee pooling
and distribution, key assignment, liveness, and genesis round-trip.

This directory is its own Go module (`tests/e2e/go.mod`) and is excluded from the
top-level `make test` and from linting. It runs via the `make test-e2e` target
described below. Docker is required.

## Running

```
make docker-build-all   # build the chain image (cosmos/vaas-e2e); ts-relayer image is pulled
make test-e2e           # docker-build-all, then go test in this directory
```

`make test-e2e`
([Makefile](../../Makefile) lines 305-322) runs:

```
cd tests/e2e && go test -timeout=60m -v ./... --count=1
```

The chain image is built from
[docker/e2e.Dockerfile](docker/e2e.Dockerfile) and contains both the provider
and consumer binaries. The relayer image is
`ghcr.io/allinbits/ibc-v2-ts-relayer:latest`, pulled at runtime
([e2e_tsrelayer_test.go](e2e_tsrelayer_test.go) lines 17-18).

To run a single suite:

```
cd tests/e2e && go test -run TestIntegrationTestSuite -v --count=1
cd tests/e2e && go test -run TestLivenessIntegrationTestSuite -v --count=1
```

## The two suites

Both are `testify` suites that embed a shared `baseTestSuite`
([base_suite_test.go](base_suite_test.go)) providing container lifecycle, chain
init, ts-relayer wiring, and exec/query helpers. Each suite launches its own
isolated set of containers (distinct chain IDs, Docker network, and host ports)
so the two can run on the same host without colliding.

### `IntegrationTestSuite` -- the main suite

Entry point `TestIntegrationTestSuite`
([e2e_setup_test.go](e2e_setup_test.go) lines 42-44). `SetupSuite` (lines 59+):
creates the Docker pool and network, initializes and starts the provider,
registers a consumer, fetches the consumer genesis from the provider,
initializes and starts the consumer, then starts the ts-relayer and creates the
IBC v2 path. Provider genesis is patched for a fast voting period, a fast epoch
(`blocks_per_epoch = 5`), a small fee amount, and shrunk downtime windows so the
challenge-gated flow completes inside a test run.

The scenarios run as an **ordered** sequence in `TestVAAS`
([e2e_test.go](e2e_test.go)): provider and consumer block production,
consumer-on-provider and provider-on-consumer, validator-set sync, a
transient-outage snapshot resync, the debt flow, downtime slash, the fee-pool
send restriction / fund-and-lock / gov-subsidy-clawback tests, fee-distribution
accrual, key assignment, a downtime challenge rejected at the sealed-signature
step, liveness removal, and finally the genesis round-trip (which stops the
provider container and restarts it from exported genesis). The order is
load-bearing: later tests depend on consumer `"0"` staying `LAUNCHED` until
liveness removal, and the genesis round-trip runs last.

This suite uses a realistic (~21-day) provider unbonding, so the liveness sweep
timing is not exercised here -- that is the liveness suite's job.

### `LivenessIntegrationTestSuite` -- observable liveness timing

Entry point `TestLivenessIntegrationTestSuite`
([e2e_liveness_suite_test.go](e2e_liveness_suite_test.go) lines 91-93). This
suite sets `LivenessGraceFraction = 0.75` and a short (200s) unbonding in
provider genesis so the grace (~150s), safe mode, forced VSC timeout + snapshot
resync, and auto-sweep removal are all observable within a CI run, while keeping
the relayer-derived client trusting period viable. It uses fast blocks and a
first-sync gate to keep the timing-sensitive assertions reliable. Its consumer
is registered via `testdata/create_consumer_short_unbonding.json`. Scenarios run
in order in `TestLivenessVAAS` (lines 208+): recover-before-grace, real safe
mode, the liveness query, forced-timeout snapshot resync, and auto-sweep
removal. The header comment of that file documents the timing rationale in
detail.

## File layout

| File | Purpose |
|---|---|
| `doc.go` | package doc |
| `base_suite_test.go` | shared `baseTestSuite`: container lifecycle, chain init, config |
| `e2e_setup_test.go` | `IntegrationTestSuite` + its `SetupSuite` / config |
| `e2e_test.go` | `TestVAAS` ordered scenario list |
| `e2e_liveness_suite_test.go` | `LivenessIntegrationTestSuite` + `TestLivenessVAAS` |
| `e2e_tsrelayer_test.go` | ts-relayer container start/stop, `add-path`, relay control |
| `e2e_vaas_test.go` | core VSC / valset-sync scenarios |
| `e2e_debt_test.go` | fee-pool debt-gating scenario |
| `e2e_downtime_slash_test.go` | downtime evidence + slash scenario |
| `e2e_downtime_challenge_test.go` | challenging a queued downtime slash with real consumer chain data |
| `e2e_fee_pool_test.go` | fee-pool send restriction, locks, gov clawback |
| `e2e_fee_distribution_test.go` | per-epoch validator payout out of a consumer's fee pool |
| `e2e_key_assignment_test.go` | consumer key assignment and the consumer switching onto it |
| `e2e_consumer_liveness_test.go` | liveness / safe-mode scenarios |
| `e2e_genesis_roundtrip_test.go` | provider export/restart round-trip |
| `gov_proposal_helpers_test.go` | submit/vote governance proposals from a test |
| `validator_identity_helpers_test.go` | one validator's address forms across both chains |
| `genesis_test.go` | genesis-file patching (consumer genesis merge, generic mutation) |
| `query_test.go`, `http_util_test.go`, `chain_test.go`, `e2e_exec_test.go`, `io.go` | query, HTTP, chain, container-exec helpers |
| `go.mod`, `go.sum` | this directory's own Go module |
| `docker/e2e.Dockerfile` | chain image (provider + consumer binaries) |
| `scripts/provider-init.sh`, `scripts/consumer-init.sh` | in-container chain init |
| `testdata/create_consumer*.json` | consumer-registration payloads (with a `CONSUMER_CHAIN_ID` placeholder) |

## Adding a scenario

1. Write a `testXxx` method on the relevant suite (`*IntegrationTestSuite`), in
   a new or existing `e2e_*.go` file. Use the base-suite helpers for container
   exec, chain queries, gov proposals, and ts-relayer control (start, pause,
   resume, `add-path`) rather than driving Docker directly.
2. Call it from the ordered sequence in `TestVAAS` (or `TestLivenessVAAS`) at
   the right point. Ordering matters: the suite shares one set of containers and
   one consumer across all scenarios, so place your test where the chain is in
   the state it needs, and before anything that tears that state down.
3. If the scenario needs specific chain configuration, extend the suite's
   `patchProviderGenesis` (or add a `testdata` payload) rather than mutating
   state mid-run.

The relayer is available at
<https://github.com/allinbits/ibc-v2-ts-relayer>.
