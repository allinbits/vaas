###############################################################################
###                                Protobuf                                 ###
###############################################################################

containerProtoVer=0.14.0
containerProtoImage=ghcr.io/cosmos/proto-builder:$(containerProtoVer)
protoImage=docker run --rm -v $(CURDIR):/workspace --workdir /workspace $(containerProtoImage)

proto-gen:
	@echo "Generating Protobuf files"
	@$(protoImage) sh ./scripts/protocgen.sh

proto-format:
	@echo "Formatting Protobuf files"
	@$(protoImage) find ./ -name "*.proto" -exec clang-format -i {} \;

proto-lint:
	@$(protoImage) buf lint --error-format=json

proto-update-deps:
	@echo "Updating Protobuf dependencies"
	@$(protoImage) buf mod update proto/

.PHONY: proto-gen proto-format proto-lint proto-update-deps
