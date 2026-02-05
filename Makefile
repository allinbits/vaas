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
	go test ./...

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

provider_home=~/.provider-localnet
providerd=./build/provider --home $(provider_home)

###############################################################################
###                                  Apps                                   ###
###############################################################################
BUILDDIR ?= $(CURDIR)/build


GO_REQUIRED_VERSION = $(shell go list -f {{.GoVersion}} -m)
BUILD_TARGETS := build-apps install-apps
build-apps: BUILD_ARGS=-o $(BUILDDIR)/

$(BUILD_TARGETS): go.sum $(BUILDDIR)/
	cd app; go $(patsubst %-apps,%,$@) -mod=readonly $(BUILD_ARGS) ./...

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
###                                 Relayer                                 ###
###############################################################################

HERMES ?= $(shell which hermes)
HERMES_CONFIG ?= $(HOME)/.vaas-hermes/config.toml

HERMES_CMD = $(HERMES) --config $(HERMES_CONFIG)

# Check Hermes IBC relayer installation
relayer-install:
	@echo "checking Hermes IBC relayer..."
	@if [ "$(HERMES)" != "" ]; then echo "Found Hermes binary"; else echo "Could not find Hermes relayer in PATH. Please follow app/README.md to install it." && exit 1; fi
	@$(HERMES) version

# Create Hermes configuration
relayer-config: relayer-install
	@chmod +x ./scripts/hermes-config.sh
	@./scripts/hermes-config.sh

# Create relayer keys and fund them (requires both chains running)
RELAYER_MNEMONIC_FILE = /tmp/vaas-test/relayer_mnemonic.txt

relayer-keys:
	@echo "Creating relayer mnemonic file..."
	@mkdir -p /tmp/vaas-test
	@echo "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art" > $(RELAYER_MNEMONIC_FILE)
	@echo "Setting up relayer account on provider..."
	@chmod +x ./scripts/add-relayer-key.sh
	@./scripts/add-relayer-key.sh ./build/provider $(provider_home)
	@echo "Funding relayer on provider..."
	$(providerd) tx bank send val $$($(providerd) keys show relayer -a) 100000000uatone --fees 5000uatone -y
	@echo "Waiting for provider tx..."
	@sleep 5
	@echo "Removing old Hermes keys if they exist..."
	@rm -rf $(HOME)/.vaas-hermes/keys
	@echo "Adding relayer key to Hermes for provider..."
	$(HERMES_CMD) keys add --chain provider-localnet --mnemonic-file $(RELAYER_MNEMONIC_FILE) --key-name relayer
	@echo "Adding relayer key to Hermes for consumer..."
	$(HERMES_CMD) keys add --chain consumer-localnet --mnemonic-file $(RELAYER_MNEMONIC_FILE) --key-name relayer
	@echo "Relayer keys setup complete"
	@echo "Relayer address: $$($(providerd) keys show relayer -a)"

# Create the CCV/VAAS channel between provider and consumer
# IMPORTANT: Must use genesis clients (07-tendermint-0) for VAAS channel
# After a fresh localnet-clean, this will create connection-0
relayer-channel:
	@echo "Creating connection using genesis clients (07-tendermint-0)..."
	$(HERMES_CMD) create connection --a-chain consumer-localnet --a-client 07-tendermint-0 --b-client 07-tendermint-0
	@sleep 2
	@echo "Creating VAAS channel on connection-0..."
	$(HERMES_CMD) create channel --a-chain consumer-localnet --a-connection connection-0 --a-port consumer --b-port provider --order ordered --channel-version 1
	@echo "Channel created"

# Start the relayer
relayer-start:
	@echo "Starting Hermes relayer..."
	$(HERMES_CMD) start

# Full relayer setup (requires both chains running)
relayer-setup: relayer-config relayer-keys relayer-channel
	@echo "Relayer setup complete. Run 'make relayer-start' to start relaying."

.PHONY: relayer-install relayer-config relayer-keys relayer-channel relayer-start relayer-setup

###############################################################################
###                              Full Localnet                              ###
###############################################################################

# Clean all localnet data
localnet-clean:
	rm -rf $(provider_home) $(consumer_home)
	@echo "Localnet data cleaned"

.PHONY: localnet-clean 
