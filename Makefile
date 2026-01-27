# command to run dependency utilities
rundep=go run -modfile devdeps/go.mod

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
	cd app; go $(patsubst %-apps,%,$@) -mod=readonly  $(BUILD_ARGS) ./...

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
	$(providerd) start

consumer_home=~/.consumer-localnet
consumerd=./build/consumer --home $(consumer_home)
consumer-start: build-apps
	rm -rf $(consumer_home)
	$(consumerd) init localnet --default-denom uatone --chain-id consumer-localnet
	$(consumerd) config set client chain-id consumer-localnet
	$(consumerd) config set client keyring-backend test
	$(consumerd) keys add val
	$(consumerd) genesis add-genesis-account val 1000000000000uatone 
	$(consumerd) keys add user
	$(consumerd) genesis add-genesis-account user 1000000000uatone 
	$(consumerd) genesis gentx val 1000000000uatone
	$(consumerd) genesis collect-gentxs

	# Set validator gas prices
	sed -i.bak 's#^minimum-gas-prices = .*#minimum-gas-prices = "0.01uatone,0.01uphoton"#g' $(consumer_home)/config/app.toml
	# disable REST API
	$(consumerd) config set app api.enable false
	# Decrease voting period to 5min
	jq '.app_state.gov.params.voting_period = "300s"' $(consumer_home)/config/genesis.json > /tmp/gen
	mv /tmp/gen $(consumer_home)/config/genesis.json
	$(consumerd) start 
