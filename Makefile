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
