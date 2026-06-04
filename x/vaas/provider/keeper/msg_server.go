package keeper

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	tmtypes "github.com/cometbft/cometbft/types"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

type msgServer struct {
	*Keeper
}

// NewMsgServerImpl returns an implementation of the bank MsgServer interface
// for the provided Keeper.
func NewMsgServerImpl(keeper *Keeper) types.MsgServer {
	return &msgServer{Keeper: keeper}
}

var _ types.MsgServer = msgServer{}

// UpdateParams updates the params.
func (k msgServer) UpdateParams(goCtx context.Context, msg *types.MsgUpdateParams) (*types.MsgUpdateParamsResponse, error) {
	if k.GetAuthority() != msg.Authority {
		return nil, errorsmod.Wrapf(govtypes.ErrInvalidSigner, "invalid authority; expected %s, got %s", k.authority, msg.Authority)
	}

	if err := msg.Params.Validate(); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	// Capture the current floor before applying the new params so we only walk
	// the overrides when the fees_per_block floor actually rises.
	oldFloor := k.GetFeesPerBlock(ctx).Amount
	k.Keeper.SetParams(ctx, msg.Params)

	// Per-consumer fees_per_block overrides must stay strictly above the global
	// default. Only a higher floor can leave an existing override underwater; an
	// unchanged or lower floor keeps every override valid, so skip the walk.
	if msg.Params.FeesPerBlock.Amount.GT(oldFloor) {
		if err := k.reconcileFeesPerBlockOverrides(ctx, msg.Params.FeesPerBlock.Amount); err != nil {
			return nil, err
		}
	}

	return &types.MsgUpdateParamsResponse{}, nil
}

// AssignConsumerKey defines a method to assign a consensus key on a consumer chain
// for a given validator on the provider
func (k msgServer) AssignConsumerKey(goCtx context.Context, msg *types.MsgAssignConsumerKey) (*types.MsgAssignConsumerKeyResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	providerValidatorAddr, err := sdk.ValAddressFromBech32(msg.ProviderAddr)
	if err != nil {
		return nil, err
	}

	// validator must already be registered
	validator, err := k.stakingKeeper.GetValidator(ctx, providerValidatorAddr)
	if err != nil && err == stakingtypes.ErrNoValidatorFound {
		return nil, stakingtypes.ErrNoValidatorFound
	} else if err != nil {
		return nil, err
	}

	consumerTMPublicKey, err := k.ParseConsumerKey(msg.ConsumerKey)
	if err != nil {
		return nil, err
	}

	if err := k.Keeper.AssignConsumerKey(ctx, msg.ConsumerId, validator, consumerTMPublicKey); err != nil {
		return nil, err
	}

	chainId, err := k.GetConsumerChainId(ctx, msg.ConsumerId)
	if err != nil {
		return nil, errorsmod.Wrapf(vaastypes.ErrInvalidConsumerState, "cannot get consumer chain ID: %s", err.Error())
	}

	k.Logger(ctx).Info("validator assigned consumer key",
		"consumerId", msg.ConsumerId,
		"chainId", chainId,
		"validator operator addr", msg.ProviderAddr,
		"consumer public key", msg.ConsumerKey,
	)

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			types.EventTypeAssignConsumerKey,
			sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
			sdk.NewAttribute(types.AttributeConsumerId, strconv.FormatUint(msg.ConsumerId, 10)),
			sdk.NewAttribute(types.AttributeConsumerChainId, chainId),
			sdk.NewAttribute(types.AttributeProviderValidatorAddress, msg.ProviderAddr),
			sdk.NewAttribute(types.AttributeConsumerConsensusPubKey, msg.ConsumerKey),
			sdk.NewAttribute(types.AttributeSubmitterAddress, msg.Signer),
		),
	)

	return &types.MsgAssignConsumerKeyResponse{}, nil
}

func (k msgServer) SubmitConsumerMisbehaviour(goCtx context.Context, msg *types.MsgSubmitConsumerMisbehaviour) (*types.MsgSubmitConsumerMisbehaviourResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	byzantineValidators, err := k.Keeper.HandleConsumerMisbehaviour(ctx, msg.ConsumerId, *msg.Misbehaviour)
	if err != nil {
		return nil, err
	}

	byzantineAddrs := make([]string, len(byzantineValidators))
	for i, addr := range byzantineValidators {
		byzantineAddrs[i] = addr.String()
	}

	chainID := msg.Misbehaviour.Header1.SignedHeader.Header.ChainID
	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			vaastypes.EventTypeSubmitConsumerMisbehaviour,
			sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
			sdk.NewAttribute(types.AttributeConsumerId, strconv.FormatUint(msg.ConsumerId, 10)),
			sdk.NewAttribute(types.AttributeConsumerChainId, chainID),
			sdk.NewAttribute(vaastypes.AttributeConsumerMisbehaviour, msg.Misbehaviour.String()),
			sdk.NewAttribute(vaastypes.AttributeMisbehaviourClientId, msg.Misbehaviour.ClientId),
			sdk.NewAttribute(vaastypes.AttributeMisbehaviourHeight1, msg.Misbehaviour.Header1.GetHeight().String()),
			sdk.NewAttribute(vaastypes.AttributeMisbehaviourHeight2, msg.Misbehaviour.Header2.GetHeight().String()),
			sdk.NewAttribute(vaastypes.AttributeByzantineValidators, strings.Join(byzantineAddrs, ",")),
			sdk.NewAttribute(types.AttributeSubmitterAddress, msg.Submitter),
		),
	)

	return &types.MsgSubmitConsumerMisbehaviourResponse{}, nil
}

func (k msgServer) SubmitConsumerDoubleVoting(goCtx context.Context, msg *types.MsgSubmitConsumerDoubleVoting) (*types.MsgSubmitConsumerDoubleVotingResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	consumerChainId, err := k.GetConsumerChainId(ctx, msg.ConsumerId)
	if err != nil {
		return nil, err
	}
	headerChainID := msg.InfractionBlockHeader.SignedHeader.Header.ChainID
	if headerChainID != consumerChainId {
		return nil, errorsmod.Wrapf(
			vaastypes.ErrInvalidDoubleVotingEvidence,
			"infraction block header chain id (%s) does not match consumer chain id (%s) (consumerId: %d)",
			headerChainID,
			consumerChainId,
			msg.ConsumerId,
		)
	}

	evidence, err := tmtypes.DuplicateVoteEvidenceFromProto(msg.DuplicateVoteEvidence)
	if err != nil {
		return nil, err
	}

	// parse the validator set of the infraction block header in order
	// to find the public key of the validator who double voted

	// get validator set
	valset, err := tmtypes.ValidatorSetFromProto(msg.InfractionBlockHeader.ValidatorSet)
	if err != nil {
		return nil, err
	}

	// look for the malicious validator in the validator set
	_, validator := valset.GetByAddress(evidence.VoteA.ValidatorAddress)
	if validator == nil {
		return nil, errorsmod.Wrapf(
			vaastypes.ErrInvalidDoubleVotingEvidence,
			"misbehaving validator %s cannot be found in the infraction block header validator set",
			evidence.VoteA.ValidatorAddress)
	}

	pubkey, err := cryptocodec.FromCmtPubKeyInterface(validator.PubKey)
	if err != nil {
		return nil, err
	}

	// handle the double voting evidence using the malicious validator's public key
	consumerId := msg.ConsumerId
	if err := k.Keeper.HandleConsumerDoubleVoting(ctx, consumerId, evidence, pubkey); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			vaastypes.EventTypeSubmitConsumerDoubleVoting,
			sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
			sdk.NewAttribute(types.AttributeConsumerId, strconv.FormatUint(msg.ConsumerId, 10)),
			sdk.NewAttribute(types.AttributeConsumerChainId, consumerChainId),
			sdk.NewAttribute(vaastypes.AttributeConsumerDoubleVoting, msg.DuplicateVoteEvidence.String()),
			sdk.NewAttribute(types.AttributeSubmitterAddress, msg.Submitter),
		),
	)

	return &types.MsgSubmitConsumerDoubleVotingResponse{}, nil
}

// CreateConsumer creates a consumer chain
func (k msgServer) CreateConsumer(goCtx context.Context, msg *types.MsgCreateConsumer) (*types.MsgCreateConsumerResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	resp := types.MsgCreateConsumerResponse{}

	// initialize an empty slice to store event attributes
	eventAttributes := []sdk.Attribute{}

	consumerId := k.Keeper.FetchAndIncrementConsumerId(ctx)

	k.Keeper.SetConsumerOwnerAddress(ctx, consumerId, msg.Submitter)
	chainIdInUse, err := k.Keeper.ChainIdInUse(ctx, msg.ChainId)
	if err != nil {
		return nil, err
	}
	if chainIdInUse {
		return nil, errorsmod.Wrapf(types.ErrDuplicateChainId,
			"chain ID %s is already registered", msg.ChainId)
	}
	k.Keeper.SetConsumerChainId(ctx, consumerId, msg.ChainId)
	k.Keeper.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_REGISTERED)

	if err := k.Keeper.SetConsumerMetadata(ctx, consumerId, msg.Metadata); err != nil {
		return &resp, errorsmod.Wrapf(types.ErrInvalidConsumerMetadata,
			"cannot set consumer metadata: %s", err.Error())
	}

	// add event attributes
	eventAttributes = append(eventAttributes, []sdk.Attribute{
		sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
		sdk.NewAttribute(types.AttributeConsumerId, strconv.FormatUint(consumerId, 10)),
		sdk.NewAttribute(types.AttributeConsumerChainId, msg.ChainId),
		sdk.NewAttribute(types.AttributeConsumerName, msg.Metadata.Name),
		sdk.NewAttribute(types.AttributeSubmitterAddress, msg.Submitter),
		sdk.NewAttribute(types.AttributeConsumerOwner, msg.Submitter),
	}...)

	// initialization parameters are optional and hence could be nil;
	// in that case, set the default
	initializationParameters := types.DefaultConsumerInitializationParameters() // default params
	if msg.InitializationParameters != nil {
		initializationParameters = *msg.InitializationParameters
	}
	if err := k.Keeper.SetConsumerInitializationParameters(ctx, consumerId, initializationParameters); err != nil {
		return &resp, errorsmod.Wrapf(types.ErrInvalidConsumerInitializationParameters,
			"cannot set consumer initialization parameters: %s", err.Error())
	}

	// add init params event attributes for validator discovery
	if !initializationParameters.SpawnTime.IsZero() {
		eventAttributes = append(eventAttributes,
			sdk.NewAttribute(types.AttributeConsumerSpawnTime, initializationParameters.SpawnTime.String()))
	}
	if len(initializationParameters.BinaryHash) > 0 {
		eventAttributes = append(eventAttributes,
			sdk.NewAttribute(types.AttributeConsumerBinaryHash, string(initializationParameters.BinaryHash)))
	}
	if len(initializationParameters.GenesisHash) > 0 {
		eventAttributes = append(eventAttributes,
			sdk.NewAttribute(types.AttributeConsumerGenesisHash, string(initializationParameters.GenesisHash)))
	}

	// Power shaping and infraction parameters removed - all validators validate all consumers
	// with default provider parameters

	if spawnTime, initialized := k.Keeper.InitializeConsumer(ctx, consumerId); initialized {
		if err := k.Keeper.PrepareConsumerForLaunch(ctx, consumerId, time.Time{}, spawnTime); err != nil {
			return &resp, errorsmod.Wrapf(vaastypes.ErrInvalidConsumerState,
				"prepare consumer for launch, consumerId(%d), spawnTime(%s): %s", consumerId, spawnTime, err.Error())
		}
	}

	// add Phase event attribute
	phase := k.GetConsumerPhase(ctx, consumerId)
	eventAttributes = append(eventAttributes, sdk.NewAttribute(types.AttributeConsumerPhase, phase.String()))

	k.Logger(ctx).Info("created consumer",
		"consumerId", consumerId,
		"chainId", msg.ChainId,
		"owner", msg.Submitter,
		"phase", phase,
		"spawn time", initializationParameters.SpawnTime,
	)

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			types.EventTypeCreateConsumer,
			eventAttributes...,
		),
	)

	resp.ConsumerId = consumerId
	return &resp, nil
}

// UpdateConsumer updates the metadata or initialization parameters of a consumer chain
func (k msgServer) UpdateConsumer(goCtx context.Context, msg *types.MsgUpdateConsumer) (*types.MsgUpdateConsumerResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	resp := types.MsgUpdateConsumerResponse{}

	// initialize an empty slice to store event attributes
	eventAttributes := []sdk.Attribute{}

	consumerId := msg.ConsumerId

	if !k.Keeper.IsConsumerActive(ctx, consumerId) {
		return &resp, errorsmod.Wrapf(types.ErrInvalidPhase,
			"cannot update consumer chain that is not in the registered, initialized, or launched phase: %d", consumerId)
	}

	ownerAddress, err := k.Keeper.GetConsumerOwnerAddress(ctx, consumerId)
	if err != nil {
		return &resp, errorsmod.Wrapf(types.ErrNoOwnerAddress, "cannot retrieve owner address %s", ownerAddress)
	}

	if msg.Owner != ownerAddress {
		return &resp, errorsmod.Wrapf(types.ErrUnauthorized, "expected owner address %s, got %s", ownerAddress, msg.Owner)
	}

	chainId, err := k.GetConsumerChainId(ctx, consumerId)
	if err != nil {
		return &resp, errorsmod.Wrapf(vaastypes.ErrInvalidConsumerState, "cannot get consumer chain ID: %s", err.Error())
	}

	isLaunched := k.GetConsumerPhase(ctx, consumerId) == types.CONSUMER_PHASE_LAUNCHED

	// When the chain is already launched, the owner can only update metadata and transfer ownership.
	// Chain-id and initialization parameters are restricted to pre-launch phases.
	if isLaunched {
		if strings.TrimSpace(msg.NewChainId) != "" {
			return &resp, errorsmod.Wrapf(types.ErrInvalidPhase,
				"cannot update chain id of a launched chain")
		}
		if msg.InitializationParameters != nil {
			return &resp, errorsmod.Wrapf(types.ErrInvalidPhase,
				"cannot update initialization parameters of a launched chain")
		}
	}

	// We only validate and use `NewChainId` if it is not empty (because `NewChainId` is an optional argument)
	// or `NewChainId` is different from the current chain id of the consumer chain.
	if strings.TrimSpace(msg.NewChainId) != "" && msg.NewChainId != chainId {
		if err = types.ValidateChainId("NewChainId", msg.NewChainId); err != nil {
			return &resp, errorsmod.Wrapf(types.ErrInvalidMsgUpdateConsumer, "invalid new chain id: %s", err.Error())
		}

		if k.IsConsumerPrelaunched(ctx, consumerId) {
			chainId = msg.NewChainId
			chainIdInUse, err := k.Keeper.ChainIdInUse(ctx, chainId)
			if err != nil {
				return nil, err
			}
			if chainIdInUse {
				return nil, errorsmod.Wrapf(types.ErrDuplicateChainId,
					"chain ID %s is already registered", chainId)
			}
			k.SetConsumerChainId(ctx, consumerId, chainId)
		} else {
			// the chain id cannot be updated if the chain is NOT in a prelaunched (i.e., registered or initialized) phase
			return &resp, errorsmod.Wrapf(types.ErrInvalidPhase, "cannot update chain id of a non-prelaunched chain: %s", k.GetConsumerPhase(ctx, consumerId))
		}
	}

	// add event attributes
	eventAttributes = append(eventAttributes, []sdk.Attribute{
		sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
		sdk.NewAttribute(types.AttributeConsumerId, strconv.FormatUint(consumerId, 10)),
		sdk.NewAttribute(types.AttributeConsumerChainId, chainId),
		sdk.NewAttribute(types.AttributeSubmitterAddress, msg.Owner),
	}...)

	// The new owner address can be empty, in which case the consumer chain does not change its owner.
	// However, if the new owner address is not empty, we verify that it's a valid account address.
	if strings.TrimSpace(msg.NewOwnerAddress) != "" {
		if _, err := k.accountKeeper.AddressCodec().StringToBytes(msg.NewOwnerAddress); err != nil {
			return &resp, errorsmod.Wrapf(types.ErrInvalidNewOwnerAddress, "invalid new owner address %s", msg.NewOwnerAddress)
		}

		k.Keeper.SetConsumerOwnerAddress(ctx, consumerId, msg.NewOwnerAddress)
	}

	if msg.Metadata != nil {
		if err := k.Keeper.SetConsumerMetadata(ctx, consumerId, *msg.Metadata); err != nil {
			return &resp, errorsmod.Wrapf(types.ErrInvalidConsumerMetadata,
				"cannot set consumer metadata: %s", err.Error())
		}

		// add Name event attribute
		eventAttributes = append(eventAttributes, sdk.NewAttribute(types.AttributeConsumerName, msg.Metadata.Name))
	}

	// get the previous spawn time so that we can remove its previously planned spawn time if a new spawn time is provided
	previousInitializationParameters, err := k.Keeper.GetConsumerInitializationParameters(ctx, consumerId)
	if err != nil {
		return &resp, errorsmod.Wrapf(vaastypes.ErrInvalidConsumerState,
			"cannot get consumer initialized parameters, consumerId(%d): %s", consumerId, err.Error())
	}
	previousSpawnTime := previousInitializationParameters.SpawnTime

	if msg.InitializationParameters != nil {
		if !k.IsConsumerPrelaunched(ctx, consumerId) {
			return &resp, errorsmod.Wrap(types.ErrInvalidMsgUpdateConsumer,
				"cannot update the initialization parameters of an an already launched chain; "+
					"do not provide any initialization parameters when updating a launched chain")
		}

		phase := k.GetConsumerPhase(ctx, consumerId)
		if msg.InitializationParameters.SpawnTime.IsZero() {
			if phase == types.CONSUMER_PHASE_INITIALIZED {
				// chain was previously ready to launch at `previousSpawnTime` so we remove the
				// consumer from getting launched and move it back to the Registered phase
				err = k.RemoveConsumerToBeLaunched(ctx, consumerId, previousSpawnTime)
				if err != nil {
					return &resp, errorsmod.Wrapf(types.ErrInvalidMsgUpdateConsumer,
						"cannot remove the consumer from being launched: %s", err.Error())
				}
				k.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_REGISTERED)
			}
		}
		// add SpawnTime event attribute
		eventAttributes = append(eventAttributes,
			sdk.NewAttribute(types.AttributeConsumerSpawnTime, msg.InitializationParameters.SpawnTime.String()))

		if len(msg.InitializationParameters.BinaryHash) > 0 {
			eventAttributes = append(eventAttributes,
				sdk.NewAttribute(types.AttributeConsumerBinaryHash, string(msg.InitializationParameters.BinaryHash)))
		}
		if len(msg.InitializationParameters.GenesisHash) > 0 {
			eventAttributes = append(eventAttributes,
				sdk.NewAttribute(types.AttributeConsumerGenesisHash, string(msg.InitializationParameters.GenesisHash)))
		}

		if err = k.Keeper.SetConsumerInitializationParameters(ctx, msg.ConsumerId, *msg.InitializationParameters); err != nil {
			return &resp, errorsmod.Wrapf(types.ErrInvalidConsumerInitializationParameters,
				"cannot set consumer initialization parameters: %s", err.Error())
		}
	}

	// Power shaping and infraction parameters removed - all validators validate all consumers
	// with default provider parameters

	currentOwnerAddress, err := k.Keeper.GetConsumerOwnerAddress(ctx, consumerId)
	if err != nil {
		return &resp, errorsmod.Wrapf(vaastypes.ErrInvalidConsumerState, "cannot retrieve owner address %s: %s", ownerAddress, err.Error())
	}

	if spawnTime, initialized := k.Keeper.InitializeConsumer(ctx, consumerId); initialized {
		if err := k.Keeper.PrepareConsumerForLaunch(ctx, consumerId, previousSpawnTime, spawnTime); err != nil {
			return &resp, errorsmod.Wrapf(vaastypes.ErrInvalidConsumerState,
				"prepare consumer for launch, consumerId(%d), previousSpawnTime(%s), spawnTime(%s): %s",
				consumerId, previousSpawnTime, spawnTime, err.Error())
		}
	}

	// add Owner event attribute
	eventAttributes = append(eventAttributes, sdk.NewAttribute(types.AttributeConsumerOwner, currentOwnerAddress))

	// add Phase event attribute
	phase := k.GetConsumerPhase(ctx, consumerId)
	eventAttributes = append(eventAttributes, sdk.NewAttribute(types.AttributeConsumerPhase, phase.String()))

	k.Logger(ctx).Info("updated consumer",
		"consumerId", consumerId,
		"chainId", chainId,
		"owner", currentOwnerAddress,
		"phase", phase,
	)

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			types.EventTypeUpdateConsumer,
			eventAttributes...,
		),
	)

	return &resp, nil
}

// RemoveConsumer defines an RPC handler method for MsgRemoveConsumer.
// Only the governance authority can remove a consumer chain.
func (k msgServer) RemoveConsumer(goCtx context.Context, msg *types.MsgRemoveConsumer) (*types.MsgRemoveConsumerResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if k.GetAuthority() != msg.Authority {
		return nil, errorsmod.Wrapf(govtypes.ErrInvalidSigner, "invalid authority; expected %s, got %s", k.authority, msg.Authority)
	}

	resp := types.MsgRemoveConsumerResponse{}

	consumerId := msg.ConsumerId

	chainId, err := k.GetConsumerChainId(ctx, consumerId)
	if err != nil {
		return &resp, errorsmod.Wrapf(vaastypes.ErrInvalidConsumerState, "cannot get consumer chain ID: %s", err.Error())
	}

	phase := k.Keeper.GetConsumerPhase(ctx, consumerId)
	if phase != types.CONSUMER_PHASE_LAUNCHED {
		return &resp, errorsmod.Wrapf(types.ErrInvalidPhase,
			"chain with consumer id: %d has to be in its launched phase", consumerId)
	}

	err = k.Keeper.StopAndPrepareForConsumerRemoval(ctx, consumerId)

	k.Logger(ctx).Info("stopped consumer",
		"consumerId", consumerId,
		"chainId", chainId,
		"phase", phase,
	)

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			types.EventTypeRemoveConsumer,
			sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
			sdk.NewAttribute(types.AttributeConsumerId, strconv.FormatUint(consumerId, 10)),
			sdk.NewAttribute(types.AttributeConsumerChainId, chainId),
			sdk.NewAttribute(types.AttributeSubmitterAddress, msg.Authority),
		),
	)

	return &resp, err
}

// SetConsumerFeesPerBlock sets or clears the per-consumer override for the
// per-block fee amount. Only the gov authority may call this.
func (k msgServer) SetConsumerFeesPerBlock(
	goCtx context.Context,
	msg *types.MsgSetConsumerFeesPerBlock,
) (*types.MsgSetConsumerFeesPerBlockResponse, error) {
	if k.GetAuthority() != msg.Authority {
		return nil, errorsmod.Wrapf(
			govtypes.ErrInvalidSigner,
			"invalid authority; expected %s, got %s", k.GetAuthority(), msg.Authority,
		)
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	// Reject unknown consumers and consumers that have already been deleted:
	// DeleteConsumerChain leaves the phase set to DELETED, so a Has check alone
	// would let a gov proposal resurrect an override that deletion erased.
	phase := k.GetConsumerPhase(ctx, msg.ConsumerId)
	if phase == types.CONSUMER_PHASE_UNSPECIFIED || phase == types.CONSUMER_PHASE_DELETED {
		return nil, errorsmod.Wrapf(types.ErrUnknownConsumerId, "consumer %d", msg.ConsumerId)
	}

	if msg.Amount == "" {
		if err := k.ConsumerFeesPerBlockOverride.Remove(ctx, msg.ConsumerId); err != nil {
			if !errors.Is(err, collections.ErrNotFound) {
				return nil, err
			}
		}
	} else {
		// ValidateBasic guarantees a parseable, non-negative amount.
		amt, _ := math.NewIntFromString(msg.Amount)
		// The module-wide fees_per_block is a floor: a per-consumer override may
		// only raise a consumer's fee above the global default, never lower it.
		// Use an empty amount to clear the override and revert to the default.
		// The floor is also re-enforced when the global default changes (see
		// reconcileFeesPerBlockOverrides), so the invariant always holds.
		defaultAmt := k.GetFeesPerBlock(ctx).Amount
		if !amt.GT(defaultAmt) {
			return nil, errorsmod.Wrapf(
				sdkerrors.ErrInvalidRequest,
				"fees-per-block override (%s) must be greater than the global fees_per_block (%s)",
				amt, defaultAmt,
			)
		}
		if err := k.ConsumerFeesPerBlockOverride.Set(ctx, msg.ConsumerId, amt); err != nil {
			return nil, err
		}
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeSetConsumerFeesPerBlock,
		sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
		sdk.NewAttribute(types.AttributeConsumerId, strconv.FormatUint(msg.ConsumerId, 10)),
		sdk.NewAttribute(types.AttributeKeyAmount, msg.Amount),
	))

	return &types.MsgSetConsumerFeesPerBlockResponse{}, nil
}
