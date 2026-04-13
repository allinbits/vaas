# command to run dependency utilities
rundep=go run -modfile devdeps/go.mod

# Note: If you see "sonic only supports go1.17~1.23" warnings, either:
# - Use Go 1.23: brew install go@1.23
# - Or ignore it - the warning is harmless, sonic falls back to stdlib JSON

###############################################################################
###                               Build & Test                              ###
###############################################################################

build:
	go build ./...

test:
	go test -timeout=25m -v $(shell go list ./... | grep -v 'github.com/allinbits/vaas/tests/e2e')

lint_cmd=$(rundep) github.com/golangci/golangci-lint/cmd/golangci-lint
lint:
	$(lint_cmd) run ./...

lint-fix:
	$(lint_cmd) run --fix --out-format=tab --issues-exit-code=0

vulncheck:
	$(rundep) golang.org/x/vuln/cmd/govulncheck ./...

mocks-gen:
	go run go.uber.org/mock/mockgen -package=keeper -destination=testutil/keeper/mocks.go -source=x/vaas/types/expected_keepers.go

.PHONY: build test lint lint-fix vulncheck mocks-gen

###############################################################################
###                                Protobuf                                 ###
###############################################################################

containerProtoVer=0.14.0
containerProtoImage=ghcr.io/cosmos/proto-builder:$(containerProtoVer)
protoImage=docker run --rm -v $(CURDIR):/workspace --workdir /workspace $(containerProtoImage)

proto-gen:
	$(protoImage) sh ./scripts/protocgen.sh

proto-format:
	$(protoImage) find ./ -name "*.proto" -exec clang-format -i {} \;

proto-lint:
	$(protoImage) buf lint --error-format=json

proto-update-deps:
	$(protoImage) buf mod update proto/

.PHONY: proto-gen proto-format proto-lint proto-update-deps

###############################################################################
###                                  Apps                                   ###
###############################################################################
BUILDDIR ?= $(CURDIR)/build
provider_home=~/.provider-localnet
providerd=./build/provider --home $(provider_home)


GO_REQUIRED_VERSION = $(shell go list -f {{.GoVersion}} -m)
BUILD_TARGETS := build-apps install-apps
build-apps: BUILD_ARGS=-o $(BUILDDIR)/

$(BUILD_TARGETS): go.sum $(BUILDDIR)/
	@cd app; go $(patsubst %-apps,%,$@) -mod=readonly $(BUILD_ARGS) ./...

$(BUILDDIR)/:
	mkdir -p $(BUILDDIR)/

.PHONY: build-apps install-apps

provider-start: build-apps
	rm -rf $(provider_home)
	$(providerd) init localnet --default-denom uatone --chain-id provider-localnet
	$(providerd) config set client chain-id provider-localnet
	$(providerd) config set client keyring-backend test
	$(providerd) keys add val
	$(providerd) genesis add-genesis-account val 1000000000000uatone
	$(providerd) keys add user
	$(providerd) genesis add-genesis-account user 1000000000uatone
	$(providerd) genesis gentx val 1000000000uatone
	$(providerd) genesis collect-gentxs

	# Set validator gas prices
	sed -i.bak 's#^minimum-gas-prices = .*#minimum-gas-prices = "0.01uatone,0.01uphoton"#g' $(provider_home)/config/app.toml
	# enable REST API
	$(providerd) config set app api.enable true
	# Decrease voting period to 5min
	jq '.app_state.gov.params.voting_period = "300s"' $(provider_home)/config/genesis.json > /tmp/gen
	mv /tmp/gen $(provider_home)/config/genesis.json

	# Decrease number of blocks per epoch (VAAS provider VCS update)
	jq '.app_state.provider.params.blocks_per_epoch = "10"' $(provider_home)/config/genesis.json > /tmp/gen
	mv /tmp/gen $(provider_home)/config/genesis.json

	$(providerd) start

consumer_home=~/.consumer-localnet
consumerd=./build/consumer --home $(consumer_home)

# Initialize consumer chain directory (no gentx - validators come from provider)
consumer-init: build-apps
	rm -rf $(consumer_home)
	$(consumerd) init localnet --default-denom uatone --chain-id consumer-localnet
	$(consumerd) config set client chain-id consumer-localnet
	$(consumerd) config set client keyring-backend test
	$(consumerd) keys add user
	@chmod +x ./scripts/add-relayer-key.sh
	@./scripts/add-relayer-key.sh ./build/consumer $(consumer_home)
	$(consumerd) genesis add-genesis-account user 1000000000uatone
	$(consumerd) genesis add-genesis-account relayer 100000000uatone
	# Set gas prices
	sed -i.bak 's#^minimum-gas-prices = .*#minimum-gas-prices = "0.01uatone,0.01uphoton"#g' $(consumer_home)/config/app.toml
	# Use different ports to avoid conflicts with provider
	sed -i.bak 's#tcp://127.0.0.1:26657#tcp://127.0.0.1:26667#g' $(consumer_home)/config/config.toml
	sed -i.bak 's#tcp://0.0.0.0:26656#tcp://0.0.0.0:26666#g' $(consumer_home)/config/config.toml
	sed -i.bak 's#tcp://127.0.0.1:26658#tcp://127.0.0.1:26668#g' $(consumer_home)/config/config.toml
	sed -i.bak 's#tcp://localhost:26657#tcp://localhost:26667#g' $(consumer_home)/config/client.toml
	sed -i.bak 's#localhost:9090#localhost:9092#g' $(consumer_home)/config/app.toml
	sed -i.bak 's#localhost:1317#localhost:1318#g' $(consumer_home)/config/app.toml
	sed -i.bak 's#localhost:6060#localhost:6062#g' $(consumer_home)/config/config.toml
	@echo "Consumer initialized. Now run 'make consumer-create' on provider to register the consumer chain."

# Create consumer chain on the provider (run after provider is started)
consumer-create:
	@echo "Creating consumer chain on provider..."
	@mkdir -p /tmp/vaas-test
	@echo '{"chain_id": "consumer-localnet", "metadata": {"name": "consumer", "description": "test consumer chain", "metadata": "{}"}, "initialization_parameters": {"initial_height": {"revision_number": 0, "revision_height": 1}, "genesis_hash": "", "binary_hash": "", "spawn_time": "2024-01-01T00:00:00Z", "unbonding_period": 1728000000000000, "vaas_timeout_period": 2419200000000000, "historical_entries": 10000, "connection_id": ""}, "infraction_parameters": {"double_sign": {"slash_fraction": "0.05", "jail_duration": 9223372036854775807, "tombstone": true}, "downtime": {"slash_fraction": "0.0001", "jail_duration": 600000000000, "tombstone": false}}}' > /tmp/vaas-test/create_consumer.json
	$(providerd) tx provider create-consumer /tmp/vaas-test/create_consumer.json --from val --gas auto --gas-adjustment 1.5 --fees 10000uatone -y
	@echo "Consumer chain created. Wait for spawn time, then run 'make consumer-genesis' to fetch the genesis."

# Fetch consumer genesis from provider and patch local genesis (consumer-id defaults to "0")
CONSUMER_ID ?= 0
consumer-genesis:
	@echo "Fetching consumer genesis for consumer-id $(CONSUMER_ID)..."
	$(providerd) query provider consumer-genesis $(CONSUMER_ID) -o json > /tmp/vaas-test/consumer_genesis.json
	@echo "Patching consumer genesis.json with provider data..."
	jq --slurpfile cg /tmp/vaas-test/consumer_genesis.json '.app_state.vaasconsumer = $$cg[0]' $(consumer_home)/config/genesis.json > /tmp/gen
	mv /tmp/gen $(consumer_home)/config/genesis.json
	@echo "Consumer genesis patched. Run 'make consumer-run' to start the consumer."

# Copy validator key from provider to consumer (required for consumer to produce blocks)
consumer-copy-val-key:
	@echo "Copying validator key from provider to consumer..."
	cp $(provider_home)/config/priv_validator_key.json $(consumer_home)/config/priv_validator_key.json
	cp $(provider_home)/config/node_key.json $(consumer_home)/config/node_key.json
	@echo "Validator key copied. Consumer will now use provider's validator."

# Start consumer chain (after genesis is patched)
consumer-run: consumer-copy-val-key
	$(consumerd) start

# Full consumer setup flow (requires provider to be running)
consumer-start: consumer-init consumer-create
	@echo "Waiting for consumer chain to be registered on provider..."
	@for i in 1 2 3 4 5 6 7 8 9 10; do \
		if $(providerd) query provider consumer-genesis 0 -o json > /dev/null 2>&1; then \
			echo "Consumer chain registered!"; \
			break; \
		fi; \
		echo "Attempt $$i: Consumer not ready yet, waiting 3 seconds..."; \
		sleep 3; \
	done
	$(MAKE) consumer-genesis CONSUMER_ID=0
	$(MAKE) consumer-run

.PHONY: consumer-init consumer-create consumer-genesis consumer-run consumer-start

###############################################################################
###                               TS Relayer                                ###
###############################################################################

TS_RELAYER ?= ghcr.io/allinbits/ibc-v2-ts-relayer:latest

ts-relayer-start:
	@echo "Starting ts-relayer..."
	@docker rm -f vaas-ts-relayer 2>/dev/null || true
	@docker run -d --name vaas-ts-relayer --network host \
		--cap-add IPC_LOCK \
		$(TS_RELAYER)
	@sleep 3
	@echo "Configuring ts-relayer..."
	@docker exec vaas-ts-relayer /bin/with_keyring ibc-v2-ts-relayer add-mnemonic \
		-c provider-localnet \
		--mnemonic "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art"
	@docker exec vaas-ts-relayer /bin/with_keyring ibc-v2-ts-relayer add-mnemonic \
		-c consumer-localnet \
		--mnemonic "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art"
	@docker exec vaas-ts-relayer /bin/with_keyring ibc-v2-ts-relayer add-gas-price \
		-c provider-localnet --gas-adjustment 2.0 0.025uatone
	@docker exec vaas-ts-relayer /bin/with_keyring ibc-v2-ts-relayer add-gas-price \
		-c consumer-localnet --gas-adjustment 2.0 0.025uatone
	@echo "Creating IBC v2 path..."
	@docker exec vaas-ts-relayer /bin/with_keyring ibc-v2-ts-relayer add-path \
		-s provider-localnet \
		-d consumer-localnet \
		--surl http://127.0.0.1:26657 \
		--durl http://127.0.0.1:26667 \
		--ibc-version 2
	@echo "ts-relayer configured and running (log: /tmp/vaas-ts-relayer.log)"

ts-relayer-stop:
	@echo "Stopping ts-relayer..."
	-@docker rm -f vaas-ts-relayer 2>/dev/null || true

.PHONY: ts-relayer-start ts-relayer-stop



###############################################################################
###                              Full Localnet                              ###
###############################################################################

# Clean all localnet data and stop running processes
localnet-clean:
	@echo "Stopping running processes..."
	-@pkill -f "provider.*start" 2>/dev/null || true
	-@pkill -f "consumer.*start" 2>/dev/null || true
	@$(MAKE) ts-relayer-stop
	@sleep 2
	rm -rf $(provider_home) $(consumer_home)
	rm -f /tmp/vaas-provider.log /tmp/vaas-consumer.log /tmp/vaas-ts-relayer.log
	rm -rf /tmp/vaas-test
	@echo "Localnet data cleaned"

# Start the full localnet: provider, consumer, and ts-relayer
localnet-start: build-apps
	@echo "=== Starting VAAS Localnet ==="
	@echo ""
	@echo "Step 1/4: Starting provider chain in background..."
	@$(MAKE) provider-start > /tmp/vaas-provider.log 2>&1 &
	@echo "  Waiting for provider to produce blocks..."
	@for i in $$(seq 1 60); do \
		HEIGHT=$$(curl -sf http://localhost:26657/status 2>/dev/null | sed -n 's/.*"latest_block_height":"\([0-9]*\)".*/\1/p'); \
		if [ -n "$$HEIGHT" ] && [ "$$HEIGHT" -gt 0 ] 2>/dev/null; then \
			echo "  Provider is producing blocks (height $$HEIGHT, after $$((i*2))s)"; \
			break; \
		fi; \
		if [ $$i -eq 60 ]; then \
			echo "  ERROR: Provider failed to produce blocks within 120s. Check /tmp/vaas-provider.log"; \
			exit 1; \
		fi; \
		sleep 2; \
	done
	@echo ""
	@echo "Step 2/4: Starting consumer chain in background..."
	@$(MAKE) consumer-start > /tmp/vaas-consumer.log 2>&1 &
	@echo "  Waiting for consumer to produce blocks..."
	@for i in $$(seq 1 90); do \
		HEIGHT=$$(curl -sf http://localhost:26667/status 2>/dev/null | sed -n 's/.*"latest_block_height":"\([0-9]*\)".*/\1/p'); \
		if [ -n "$$HEIGHT" ] && [ "$$HEIGHT" -gt 0 ] 2>/dev/null; then \
			echo "  Consumer is producing blocks (height $$HEIGHT, after $$((i*2))s)"; \
			break; \
		fi; \
		if [ $$i -eq 90 ]; then \
			echo "  ERROR: Consumer failed to produce blocks within 180s. Check /tmp/vaas-consumer.log"; \
			exit 1; \
		fi; \
		sleep 2; \
	done
	@echo ""
	@echo "Step 3/4: Starting ts-relayer..."
	@$(MAKE) ts-relayer-start > /tmp/vaas-ts-relayer.log 2>&1
	@echo ""
	@echo "Step 4/4: Triggering valset change to send first VSC packet..."
	@VALOPER=$$($(providerd) keys show val --bech val -a 2> /dev/null); \
	$(providerd) tx staking delegate $$VALOPER 1000000uatone --from user --fees 5000uatone -y > /dev/null 2>&1
	@echo "  Delegation sent. Waiting for VSC packet to be relayed..."
	@for i in $$(seq 1 60); do \
		if $(consumerd) query vaasconsumer provider-info --node tcp://localhost:26667 > /dev/null 2>&1; then \
			echo "  Channel established! (after $$((i*2))s)"; \
			break; \
		fi; \
		if [ $$i -eq 60 ]; then \
			echo "  WARNING: VSC packet not relayed within 120s. The channel may need more time."; \
			break; \
		fi; \
		sleep 2; \
	done
	@echo ""
	@echo "=== VAAS Localnet is running! ==="
	@echo ""
	@echo "  Provider: http://localhost:26657 (log: /tmp/vaas-provider.log)"
	@echo "  Consumer: http://localhost:26667 (log: /tmp/vaas-consumer.log)"
	@echo "  Relayer:  ts-relayer container   (log: /tmp/vaas-ts-relayer.log)"
	@echo ""
	@echo "  To stop: make localnet-clean"

.PHONY: localnet-clean localnet-start

###############################################################################
###                            Docker E2E Tests                             ###
###############################################################################

# Build the chain Docker image (provider + consumer binaries)
docker-build-debug:
	@echo "Building VAAS e2e chain image..."
	docker build -t cosmos/vaas-e2e -f tests/e2e/docker/e2e.Dockerfile .

# Build all Docker images needed for e2e tests
docker-build-all: docker-build-debug

# Run the e2e integration test suite
test-e2e: docker-build-all
	@echo "Running e2e tests..."
	cd tests/e2e && go test -timeout=25m -v ./... --count=1

.PHONY: docker-build-debug docker-build-all test-e2e
