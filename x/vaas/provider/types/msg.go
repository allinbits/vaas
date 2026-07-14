package types

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	tmtypes "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

const (
	// MaxNameLength defines the maximum consumer name length
	MaxNameLength = 50
	// MaxDescriptionLength defines the maximum consumer description length
	MaxDescriptionLength = 10000
	// MaxMetadataLength defines the maximum consumer metadata length
	MaxMetadataLength = 255
	// MaxHashLength defines the maximum length of a hash
	MaxHashLength = 64
	// MaxValidatorCount defines the maximum number of validators
	MaxValidatorCount = 1000
)

var (
	_ sdk.Msg = (*MsgAssignConsumerKey)(nil)
	_ sdk.Msg = (*MsgSubmitConsumerMisbehaviour)(nil)
	_ sdk.Msg = (*MsgSubmitConsumerDoubleVoting)(nil)
	_ sdk.Msg = (*MsgCreateConsumer)(nil)
	_ sdk.Msg = (*MsgUpdateConsumer)(nil)
	_ sdk.Msg = (*MsgRemoveConsumer)(nil)
	_ sdk.Msg = (*MsgFundConsumerFeePool)(nil)
	_ sdk.Msg = (*MsgWithdrawConsumerFeePool)(nil)
	_ sdk.Msg = (*MsgSweepConsumerFeePool)(nil)

	_ sdk.HasValidateBasic = (*MsgAssignConsumerKey)(nil)
	_ sdk.HasValidateBasic = (*MsgSubmitConsumerMisbehaviour)(nil)
	_ sdk.HasValidateBasic = (*MsgSubmitConsumerDoubleVoting)(nil)
	_ sdk.HasValidateBasic = (*MsgCreateConsumer)(nil)
	_ sdk.HasValidateBasic = (*MsgUpdateConsumer)(nil)
	_ sdk.HasValidateBasic = (*MsgRemoveConsumer)(nil)
	_ sdk.HasValidateBasic = (*MsgSetConsumerFeesPerBlock)(nil)
	_ sdk.HasValidateBasic = (*MsgFundConsumerFeePool)(nil)
	_ sdk.HasValidateBasic = (*MsgWithdrawConsumerFeePool)(nil)
	_ sdk.HasValidateBasic = (*MsgSweepConsumerFeePool)(nil)
)

// NewMsgAssignConsumerKey creates a new MsgAssignConsumerKey instance.
// Delegator address and validator address are the same.
func NewMsgAssignConsumerKey(consumerId uint64, providerValidatorAddress sdk.ValAddress,
	consumerConsensusPubKey, signer string,
) (*MsgAssignConsumerKey, error) {
	return &MsgAssignConsumerKey{
		ConsumerId:   consumerId,
		ProviderAddr: providerValidatorAddress.String(),
		ConsumerKey:  consumerConsensusPubKey,
		Signer:       signer,
	}, nil
}

// ValidateBasic implements the sdk.HasValidateBasic interface.
func (msg MsgAssignConsumerKey) ValidateBasic() error {
	if err := validateProviderAddress(msg.ProviderAddr, msg.Signer); err != nil {
		return errorsmod.Wrapf(ErrInvalidMsgAssignConsumerKey, "ProviderAddr: %s", err.Error())
	}

	if msg.ConsumerKey == "" {
		return errorsmod.Wrapf(ErrInvalidMsgAssignConsumerKey, "ConsumerKey cannot be empty")
	}
	if _, _, err := ParseConsumerKeyFromJson(msg.ConsumerKey); err != nil {
		return errorsmod.Wrapf(ErrInvalidMsgAssignConsumerKey, "ConsumerKey: %s", err.Error())
	}

	return nil
}

func NewMsgSubmitConsumerMisbehaviour(
	consumerId uint64,
	submitter sdk.AccAddress,
	misbehaviour *ibctmtypes.Misbehaviour,
) (*MsgSubmitConsumerMisbehaviour, error) {
	return &MsgSubmitConsumerMisbehaviour{
		Submitter:    submitter.String(),
		Misbehaviour: misbehaviour,
		ConsumerId:   consumerId,
	}, nil
}

// ValidateBasic implements the sdk.HasValidateBasic interface.
func (msg MsgSubmitConsumerMisbehaviour) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Submitter); err != nil {
		return errorsmod.Wrapf(ErrInvalidMsgSubmitConsumerMisbehaviour, "Submitter: %s", err.Error())
	}

	if msg.Misbehaviour == nil {
		return errorsmod.Wrapf(ErrInvalidMsgSubmitConsumerMisbehaviour, "Misbehaviour cannot be nil")
	}
	if err := msg.Misbehaviour.ValidateBasic(); err != nil {
		return errorsmod.Wrapf(ErrInvalidMsgSubmitConsumerMisbehaviour, "Misbehaviour: %s", err.Error())
	}
	return nil
}

func NewMsgSubmitConsumerDoubleVoting(
	consumerId uint64,
	submitter sdk.AccAddress,
	ev *tmtypes.DuplicateVoteEvidence,
	header *ibctmtypes.Header,
) (*MsgSubmitConsumerDoubleVoting, error) {
	return &MsgSubmitConsumerDoubleVoting{
		Submitter:             submitter.String(),
		DuplicateVoteEvidence: ev,
		InfractionBlockHeader: header,
		ConsumerId:            consumerId,
	}, nil
}

// ValidateBasic implements the sdk.HasValidateBasic interface.
func (msg MsgSubmitConsumerDoubleVoting) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Submitter); err != nil {
		return errorsmod.Wrapf(ErrInvalidMsgSubmitConsumerDoubleVoting, "Submitter: %s", err.Error())
	}

	dve, err := cmttypes.DuplicateVoteEvidenceFromProto(msg.DuplicateVoteEvidence)
	if err != nil {
		return errorsmod.Wrapf(ErrInvalidMsgSubmitConsumerDoubleVoting, "DuplicateVoteEvidence: %s", err.Error())
	}
	if err := dve.ValidateBasic(); err != nil {
		return errorsmod.Wrapf(ErrInvalidMsgSubmitConsumerDoubleVoting, "DuplicateVoteEvidence: %s", err.Error())
	}

	if err := ValidateHeaderForConsumerDoubleVoting(msg.InfractionBlockHeader); err != nil {
		return errorsmod.Wrapf(ErrInvalidMsgSubmitConsumerDoubleVoting, "ValidateTendermintHeader: %s", err.Error())
	}

	// CometBFT's DuplicateVoteEvidence.ValidateBasic does not enforce that the
	// two votes share height/round/type, so check it here before any signature
	// verification runs against mismatched payloads.
	if dve.VoteA.Height != dve.VoteB.Height ||
		dve.VoteA.Round != dve.VoteB.Round ||
		dve.VoteA.Type != dve.VoteB.Type {
		return errorsmod.Wrapf(
			ErrInvalidMsgSubmitConsumerDoubleVoting,
			"duplicate vote evidence height/round/type mismatch: %d/%d/%v vs %d/%d/%v",
			dve.VoteA.Height, dve.VoteA.Round, dve.VoteA.Type,
			dve.VoteB.Height, dve.VoteB.Round, dve.VoteB.Type,
		)
	}

	header := msg.InfractionBlockHeader.SignedHeader.Header
	if header.Height != dve.VoteA.Height {
		return errorsmod.Wrapf(
			ErrInvalidMsgSubmitConsumerDoubleVoting,
			"infraction block header height (%d) does not match duplicate vote evidence height (%d)",
			header.Height,
			dve.VoteA.Height,
		)
	}

	// Validate that evidence signatures are valid for the infraction header chain ID.
	valset, err := cmttypes.ValidatorSetFromProto(msg.InfractionBlockHeader.ValidatorSet)
	if err != nil {
		return errorsmod.Wrapf(ErrInvalidMsgSubmitConsumerDoubleVoting, "InfractionBlockHeader.ValidatorSet: %s", err.Error())
	}
	_, validator := valset.GetByAddress(dve.VoteA.ValidatorAddress)
	if validator == nil {
		return errorsmod.Wrapf(
			ErrInvalidMsgSubmitConsumerDoubleVoting,
			"misbehaving validator %X cannot be found in infraction block header validator set",
			dve.VoteA.ValidatorAddress,
		)
	}

	headerChainID := header.ChainID
	if err := dve.VoteA.Verify(headerChainID, validator.PubKey); err != nil {
		return errorsmod.Wrapf(ErrInvalidMsgSubmitConsumerDoubleVoting, "DuplicateVoteEvidence.VoteA: %s", err.Error())
	}
	if err := dve.VoteB.Verify(headerChainID, validator.PubKey); err != nil {
		return errorsmod.Wrapf(ErrInvalidMsgSubmitConsumerDoubleVoting, "DuplicateVoteEvidence.VoteB: %s", err.Error())
	}

	return nil
}

// NewMsgCreateConsumer creates a new MsgCreateConsumer instance
func NewMsgCreateConsumer(submitter, chainId string, metadata ConsumerMetadata,
	initializationParameters *ConsumerInitializationParameters,
) (*MsgCreateConsumer, error) {
	return &MsgCreateConsumer{
		Submitter:                submitter,
		ChainId:                  chainId,
		Metadata:                 metadata,
		InitializationParameters: initializationParameters,
	}, nil
}

// IsReservedChainId returns true if the specific chain id is reserved and cannot be used by other consumer chains
func IsReservedChainId(chainId string) bool {
	// With permissionless ICS, we can have multiple consumer chains with the exact same chain id.
	// However, as we already have the Neutron and Stride Top N chains running, as a first step we would like to
	// prevent permissionless chains from re-using the chain ids of Neutron and Stride. Note that this is just a
	// preliminary measure that will be removed later on as part of:
	// TODO (#2242): find a better way of ignoring past misbehaviors
	return chainId == "neutron-1" || chainId == "stride-1"
}

// ValidateChainId validates that the chain id is valid and is not reserved.
// Can be called for the `MsgUpdateConsumer.NewChainId` field as well, so this method takes the `field` as an argument
// to return more appropriate error messages in case the validation fails.
func ValidateChainId(field, chainId string) error {
	if err := ValidateStringField(field, chainId, cmttypes.MaxChainIDLen); err != nil {
		return errorsmod.Wrapf(ErrInvalidMsgCreateConsumer, "%s: %s", field, err.Error())
	}

	if IsReservedChainId(chainId) {
		return errorsmod.Wrapf(ErrInvalidMsgCreateConsumer, "cannot use a reserved chain id")
	}

	return nil
}

// ValidateBasic implements the sdk.HasValidateBasic interface.
func (msg MsgCreateConsumer) ValidateBasic() error {
	if err := ValidateChainId("ChainId", msg.ChainId); err != nil {
		return errorsmod.Wrapf(ErrInvalidMsgCreateConsumer, "ChainId: %s", err.Error())
	}

	if err := ValidateConsumerMetadata(msg.Metadata); err != nil {
		return errorsmod.Wrapf(ErrInvalidMsgCreateConsumer, "Metadata: %s", err.Error())
	}

	if msg.InitializationParameters != nil {
		if err := ValidateInitializationParameters(*msg.InitializationParameters); err != nil {
			return errorsmod.Wrapf(ErrInvalidMsgCreateConsumer, "InitializationParameters: %s", err.Error())
		}
	}

	return nil
}

// NewMsgUpdateConsumer creates a new MsgUpdateConsumer instance
func NewMsgUpdateConsumer(owner string, consumerId uint64, ownerAddress string, metadata *ConsumerMetadata,
	initializationParameters *ConsumerInitializationParameters,
	newChainId string,
) (*MsgUpdateConsumer, error) {
	return &MsgUpdateConsumer{
		Owner:                    owner,
		ConsumerId:               consumerId,
		NewOwnerAddress:          ownerAddress,
		Metadata:                 metadata,
		InitializationParameters: initializationParameters,
		NewChainId:               newChainId,
	}, nil
}

// ValidateBasic implements the sdk.HasValidateBasic interface.
func (msg MsgUpdateConsumer) ValidateBasic() error {
	// Note that NewOwnerAddress is validated when handling the message in UpdateConsumer

	if msg.Metadata != nil {
		if err := ValidateConsumerMetadata(*msg.Metadata); err != nil {
			return errorsmod.Wrapf(ErrInvalidMsgUpdateConsumer, "Metadata: %s", err.Error())
		}
	}

	if msg.InitializationParameters != nil {
		if err := ValidateInitializationParameters(*msg.InitializationParameters); err != nil {
			return errorsmod.Wrapf(ErrInvalidMsgUpdateConsumer, "InitializationParameters: %s", err.Error())
		}
	}

	return nil
}

// NewMsgRemoveConsumer creates a new MsgRemoveConsumer instance
func NewMsgRemoveConsumer(authority string, consumerId uint64) (*MsgRemoveConsumer, error) {
	return &MsgRemoveConsumer{
		Authority:  authority,
		ConsumerId: consumerId,
	}, nil
}

// ValidateBasic implements the sdk.HasValidateBasic interface.
func (msg MsgRemoveConsumer) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Authority); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid authority address: %s", err)
	}
	return nil
}

// ValidateBasic enforces:
//   - authority parses as bech32
//   - amount is empty (clear semantics) OR parses as a positive math.Int
func (msg MsgSetConsumerFeesPerBlock) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Authority); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid authority address: %s", err)
	}
	if msg.Amount == "" {
		return nil
	}
	amt, ok := math.NewIntFromString(msg.Amount)
	if !ok {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "amount %q is not a valid integer", msg.Amount)
	}
	if !amt.IsPositive() {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "amount %q must be positive", msg.Amount)
	}
	return nil
}

//
// Validation methods
//

// ParseConsumerKeyFromJson parses the consumer key from a JSON string,
// this replaces deserializing a protobuf any.
func ParseConsumerKeyFromJson(jsonStr string) (pkType, key string, err error) {
	type PubKey struct {
		Type string `json:"@type"`
		Key  string `json:"key"`
	}
	var pubKey PubKey
	err = json.Unmarshal([]byte(jsonStr), &pubKey)
	if err != nil {
		return "", "", err
	}
	return pubKey.Type, pubKey.Key, nil
}

// ValidateHeaderForConsumerDoubleVoting validates Tendermint light client header
// for consumer double voting evidence.
//
// TODO create unit test
func ValidateHeaderForConsumerDoubleVoting(header *ibctmtypes.Header) error {
	if header == nil {
		return errors.New("infraction block header cannot be nil")
	}

	if header.SignedHeader == nil || header.SignedHeader.Header == nil {
		return errors.New("infraction block header signed header or header cannot be nil")
	}

	if header.ValidatorSet == nil {
		return errors.New("infraction block header validator set cannot be nil")
	}

	if err := header.ValidateBasic(); err != nil {
		return err
	}

	return nil
}

// ValidateStringField validates that a string `field` satisfies the following properties:
//   - is not empty
//   - has at most `maxLength` characters
func ValidateStringField(nameOfTheField, field string, maxLength int) error {
	if strings.TrimSpace(field) == "" {
		return fmt.Errorf("%s cannot be empty", nameOfTheField)
	} else if len(field) > maxLength {
		return fmt.Errorf("%s is too long; got: %d, max: %d", nameOfTheField, len(field), maxLength)
	}
	return nil
}

// TruncateString truncates a string to maximum length characters
func TruncateString(str string, maxLength int) string {
	if maxLength <= 0 {
		return ""
	}

	var truncated strings.Builder
	count := 0
	for _, char := range str {
		truncated.WriteString(string(char))
		count++
		if count >= maxLength {
			break
		}
	}
	return truncated.String()
}

// ValidateConsumerMetadata validates that all the provided metadata are in the expected range
func ValidateConsumerMetadata(metadata ConsumerMetadata) error {
	if err := ValidateStringField("name", metadata.Name, MaxNameLength); err != nil {
		return errorsmod.Wrapf(ErrInvalidConsumerMetadata, "Name: %s", err.Error())
	}

	if err := ValidateStringField("description", metadata.Description, MaxDescriptionLength); err != nil {
		return errorsmod.Wrapf(ErrInvalidConsumerMetadata, "Description: %s", err.Error())
	}

	if err := ValidateStringField("metadata", metadata.Metadata, MaxMetadataLength); err != nil {
		return errorsmod.Wrapf(ErrInvalidConsumerMetadata, "Metadata: %s", err.Error())
	}

	return nil
}

// ValidateInitializationParameters validates that all the provided parameters are in the expected range
func ValidateInitializationParameters(initializationParameters ConsumerInitializationParameters) error {
	if initializationParameters.InitialHeight.IsZero() {
		return errorsmod.Wrap(ErrInvalidConsumerInitializationParameters, "InitialHeight cannot be zero")
	}

	if err := ValidateByteSlice(initializationParameters.GenesisHash, MaxHashLength); err != nil {
		return errorsmod.Wrapf(ErrInvalidConsumerInitializationParameters, "GenesisHash: %s", err.Error())
	}

	if err := ValidateByteSlice(initializationParameters.BinaryHash, MaxHashLength); err != nil {
		return errorsmod.Wrapf(ErrInvalidConsumerInitializationParameters, "BinaryHash: %s", err.Error())
	}

	if err := vaastypes.ValidatePositiveInt64(initializationParameters.HistoricalEntries); err != nil {
		return errorsmod.Wrapf(ErrInvalidConsumerInitializationParameters, "HistoricalEntries: %s", err.Error())
	}

	if err := vaastypes.ValidateVAASTimeoutPeriod(initializationParameters.VaasTimeoutPeriod, channeltypesv2.MaxTimeoutDelta); err != nil {
		return errorsmod.Wrapf(ErrInvalidConsumerInitializationParameters, "VaasTimeoutPeriod: %s", err.Error())
	}

	if err := vaastypes.ValidateDuration(initializationParameters.UnbondingPeriod); err != nil {
		return errorsmod.Wrapf(ErrInvalidConsumerInitializationParameters, "UnbondingPeriod: %s", err.Error())
	}

	if err := vaastypes.ValidateDuration(initializationParameters.SafeModeThreshold); err != nil {
		return errorsmod.Wrapf(ErrInvalidConsumerInitializationParameters, "SafeModeThreshold: %s", err.Error())
	}

	return nil
}

func ValidateByteSlice(hash []byte, maxLength int) error {
	if len(hash) > maxLength {
		return fmt.Errorf("hash is too long; got: %d, max: %d", len(hash), maxLength)
	}
	return nil
}

// validateProviderAddress validates that the address is a sdk.ValAddress in Bech32 string format
func validateProviderAddress(addr, signer string) error {
	valAddr, err := sdk.ValAddressFromBech32(addr)
	if err != nil {
		return fmt.Errorf("invalid ValAddress (%s)", addr)
	}

	// Check that the provider validator address and the signer address are the same
	accAddr := sdk.AccAddress(valAddr.Bytes()).String()
	if accAddr != signer {
		return fmt.Errorf("ValAddress converted to AccAddress (%s) must match the signer address (%s)", accAddr, signer)
	}

	return nil
}

func ValidateInitialHeight(initialHeight clienttypes.Height, chainID string) error {
	revision := clienttypes.ParseChainID(chainID)
	if initialHeight.RevisionNumber != revision {
		return fmt.Errorf("chain ID (%s) doesn't match revision number (%d)", chainID, initialHeight.RevisionNumber)
	}
	return nil
}

// ValidateBasic implements the sdk.HasValidateBasic interface.
func (msg MsgFundConsumerFeePool) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Signer); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid signer: %s", err)
	}
	if err := msg.Amount.Validate(); err != nil {
		return errorsmod.Wrapf(ErrInvalidFundDenom, "invalid amount: %s", err)
	}
	if !msg.Amount.IsPositive() {
		return errorsmod.Wrap(ErrInvalidFundDenom, "amount must be positive")
	}
	return nil
}

// ValidateBasic implements the sdk.HasValidateBasic interface.
func (msg MsgWithdrawConsumerFeePool) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Signer); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid signer: %s", err)
	}
	if len(msg.Amount) == 0 {
		return errorsmod.Wrap(ErrInvalidFundDenom, "amount must not be empty")
	}
	if err := msg.Amount.Validate(); err != nil {
		return errorsmod.Wrapf(ErrInvalidFundDenom, "invalid amount: %s", err)
	}
	if !msg.Amount.IsAllPositive() {
		return errorsmod.Wrap(ErrInvalidFundDenom, "amounts must be positive")
	}
	return nil
}

// ValidateBasic implements the sdk.HasValidateBasic interface.
func (msg MsgSweepConsumerFeePool) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Signer); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid signer: %s", err)
	}
	seen := make(map[string]struct{}, len(msg.Denoms))
	for _, d := range msg.Denoms {
		if err := sdk.ValidateDenom(d); err != nil {
			return errorsmod.Wrapf(ErrInvalidFundDenom, "invalid denom %q: %s", d, err)
		}
		if _, dup := seen[d]; dup {
			return errorsmod.Wrapf(ErrInvalidFundDenom, "duplicate denom %q", d)
		}
		seen[d] = struct{}{}
	}
	return nil
}
