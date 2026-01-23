package keeper

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"sort"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/store/prefix"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"

	"github.com/allinbits/vaas/x/ccv/provider/types"
	ccvtypes "github.com/allinbits/vaas/x/ccv/types"
)

var _ types.QueryServer = Keeper{}

func (k Keeper) QueryConsumerGenesis(c context.Context, req *types.QueryConsumerGenesisRequest) (*types.QueryConsumerGenesisResponse, error) {
	ctx := sdk.UnwrapSDKContext(c)

	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}

	consumerId := req.ConsumerId
	if err := ccvtypes.ValidateConsumerId(consumerId); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	gen, ok := k.GetConsumerGenesis(ctx, consumerId)
	if !ok {
		return nil, status.Error(
			codes.NotFound,
			errorsmod.Wrap(types.ErrUnknownConsumerId, consumerId).Error(),
		)
	}

	return &types.QueryConsumerGenesisResponse{GenesisState: gen}, nil
}

func (k Keeper) QueryConsumerChains(goCtx context.Context, req *types.QueryConsumerChainsRequest) (*types.QueryConsumerChainsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	var chains []*types.Chain

	store := ctx.KVStore(k.storeKey)
	storePrefix := types.ConsumerIdToPhaseKeyPrefix()
	consumerPhaseStore := prefix.NewStore(store, []byte{storePrefix})
	pageRes, err := query.FilteredPaginate(consumerPhaseStore, req.Pagination, func(key, value []byte, accumulate bool) (bool, error) {
		consumerId, err := types.ParseStringIdWithLenKey(storePrefix, append([]byte{storePrefix}, key...))
		if err != nil {
			return false, status.Error(codes.Internal, err.Error())
		}

		phase := types.ConsumerPhase(binary.BigEndian.Uint32(value))
		if req.Phase != types.CONSUMER_PHASE_UNSPECIFIED && req.Phase != phase {
			return false, nil
		}

		c, err := k.GetConsumerChain(ctx, consumerId)
		if err != nil {
			return false, status.Error(codes.Internal, err.Error())
		}

		if accumulate {
			chains = append(chains, &c)
		}
		return true, nil
	})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryConsumerChainsResponse{Chains: chains, Pagination: pageRes}, nil
}

// GetConsumerChain returns a Chain data structure with all the necessary fields
func (k Keeper) GetConsumerChain(ctx sdk.Context, consumerId string) (types.Chain, error) {
	chainID, err := k.GetConsumerChainId(ctx, consumerId)
	if err != nil {
		return types.Chain{}, fmt.Errorf("cannot find chainID for consumer (%s)", consumerId)
	}

	clientID, _ := k.GetConsumerClientId(ctx, consumerId)

	metadata, err := k.GetConsumerMetadata(ctx, consumerId)
	if err != nil {
		return types.Chain{}, fmt.Errorf("cannot find metadata (%s): %s", consumerId, err.Error())
	}

	infractionParameters, err := types.DefaultConsumerInfractionParameters(ctx, k.slashingKeeper)
	if err != nil {
		return types.Chain{}, fmt.Errorf("cannot get default infraction parameters: %s", err.Error())
	}

	return types.Chain{
		ChainId:              chainID,
		ClientId:             clientID,
		Phase:                k.GetConsumerPhase(ctx, consumerId).String(),
		Metadata:             metadata,
		ConsumerId:           consumerId,
		InfractionParameters: &infractionParameters,
	}, nil
}

func (k Keeper) QueryValidatorConsumerAddr(goCtx context.Context, req *types.QueryValidatorConsumerAddrRequest) (*types.QueryValidatorConsumerAddrResponse, error) {
	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	consumerId := req.ConsumerId
	if err := ccvtypes.ValidateConsumerId(consumerId); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	providerAddrTmp, err := sdk.ConsAddressFromBech32(req.ProviderAddress)
	if err != nil {
		return nil, err
	}
	providerAddr := types.NewProviderConsAddress(providerAddrTmp)

	consumerKey, found := k.GetValidatorConsumerPubKey(ctx, consumerId, providerAddr)
	if !found {
		return &types.QueryValidatorConsumerAddrResponse{}, nil
	}

	consumerAddr, err := ccvtypes.TMCryptoPublicKeyToConsAddr(consumerKey)
	if err != nil {
		return nil, err
	}

	return &types.QueryValidatorConsumerAddrResponse{
		ConsumerAddress: consumerAddr.String(),
	}, nil
}

func (k Keeper) QueryValidatorProviderAddr(goCtx context.Context, req *types.QueryValidatorProviderAddrRequest) (*types.QueryValidatorProviderAddrResponse, error) {
	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	consumerAddrTmp, err := sdk.ConsAddressFromBech32(req.ConsumerAddress)
	if err != nil {
		return nil, err
	}
	consumerAddr := types.NewConsumerConsAddress(consumerAddrTmp)

	providerAddr, found := k.GetValidatorByConsumerAddr(ctx, req.ConsumerId, consumerAddr)
	if !found {
		return &types.QueryValidatorProviderAddrResponse{}, nil
	}

	return &types.QueryValidatorProviderAddrResponse{
		ProviderAddress: providerAddr.String(),
	}, nil
}

func (k Keeper) QueryAllPairsValConsAddrByConsumer(
	goCtx context.Context,
	req *types.QueryAllPairsValConsAddrByConsumerRequest,
) (*types.QueryAllPairsValConsAddrByConsumerResponse, error) {
	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}

	consumerId := req.ConsumerId
	if err := ccvtypes.ValidateConsumerId(consumerId); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	pairValConAddrs := []*types.PairValConAddrProviderAndConsumer{}

	ctx := sdk.UnwrapSDKContext(goCtx)
	validatorConsumerPubKeys := k.GetAllValidatorConsumerPubKeys(ctx, &consumerId)
	for _, data := range validatorConsumerPubKeys {
		consumerAddr, err := ccvtypes.TMCryptoPublicKeyToConsAddr(*data.ConsumerKey)
		if err != nil {
			return nil, err
		}
		pairValConAddrs = append(pairValConAddrs, &types.PairValConAddrProviderAndConsumer{
			ProviderAddress: sdk.ConsAddress(data.ProviderAddr).String(),
			ConsumerAddress: consumerAddr.String(),
			ConsumerKey:     data.ConsumerKey,
		})
	}

	return &types.QueryAllPairsValConsAddrByConsumerResponse{
		PairValConAddr: pairValConAddrs,
	}, nil
}

func (k Keeper) QueryParams(goCtx context.Context, req *types.QueryParamsRequest) (*types.QueryParamsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	params := k.GetParams(ctx)

	return &types.QueryParamsResponse{Params: params}, nil
}

func (k Keeper) QueryConsumerValidators(goCtx context.Context, req *types.QueryConsumerValidatorsRequest) (*types.QueryConsumerValidatorsResponse, error) {
	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}

	consumerId := req.ConsumerId
	if err := ccvtypes.ValidateConsumerId(consumerId); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	phase := k.GetConsumerPhase(ctx, consumerId)
	if phase == types.CONSUMER_PHASE_UNSPECIFIED {
		return nil, status.Errorf(codes.InvalidArgument, "cannot find a phase for consumer: %s", consumerId)
	}

	var consumerValSet []types.ConsensusValidator
	var err error

	if phase == types.CONSUMER_PHASE_LAUNCHED {
		consumerValSet, err = k.GetConsumerValSet(ctx, consumerId)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		sort.Slice(consumerValSet, func(i, j int) bool {
			return bytes.Compare(
				consumerValSet[i].ProviderConsAddr,
				consumerValSet[j].ProviderConsAddr,
			) == -1
		})
	}

	var validators []*types.QueryConsumerValidatorsValidator
	for _, consumerVal := range consumerValSet {
		provAddr := types.ProviderConsAddress{Address: consumerVal.ProviderConsAddr}
		consAddr := provAddr.ToSdkConsAddr()

		providerVal, err := k.stakingKeeper.GetValidatorByConsAddr(ctx, consAddr)
		if err != nil {
			k.Logger(ctx).Error("cannot find consensus address for provider address:%s", provAddr.String())
			continue
		}

		hasToValidate := k.IsConsumerValidator(ctx, consumerId, provAddr)

		consumerRate, found := k.GetConsumerCommissionRate(ctx, consumerId, types.NewProviderConsAddress(consAddr))
		if !found {
			consumerRate = providerVal.Commission.Rate
		}

		validators = append(validators, &types.QueryConsumerValidatorsValidator{
			ProviderAddress:         sdk.ConsAddress(consumerVal.ProviderConsAddr).String(),
			ConsumerKey:             consumerVal.PublicKey,
			ConsumerPower:           consumerVal.Power,
			ConsumerCommissionRate:  consumerRate,
			Description:             providerVal.Description,
			ProviderOperatorAddress: providerVal.OperatorAddress,
			Jailed:                  providerVal.Jailed,
			Status:                  providerVal.Status,
			ProviderTokens:          providerVal.Tokens,
			ProviderCommissionRate:  providerVal.Commission.Rate,
			ProviderPower:           providerVal.GetConsensusPower(k.stakingKeeper.PowerReduction(ctx)),
			ValidatesCurrentEpoch:   hasToValidate,
		})
	}
	return &types.QueryConsumerValidatorsResponse{
		Validators: validators,
	}, nil
}

func (k Keeper) QueryBlocksUntilNextEpoch(goCtx context.Context, req *types.QueryBlocksUntilNextEpochRequest) (*types.QueryBlocksUntilNextEpochResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	blocksUntilNextEpoch := k.BlocksUntilNextEpoch(ctx)
	return &types.QueryBlocksUntilNextEpochResponse{BlocksUntilNextEpoch: uint64(blocksUntilNextEpoch)}, nil
}

func (k Keeper) QueryConsumerIdFromClientId(goCtx context.Context, req *types.QueryConsumerIdFromClientIdRequest) (*types.QueryConsumerIdFromClientIdResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	consumerId, found := k.GetClientIdToConsumerId(ctx, req.ClientId)
	if !found {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("no known consumer chain for this client id: %s", req.ClientId))
	}

	return &types.QueryConsumerIdFromClientIdResponse{ConsumerId: consumerId}, nil
}

func (k Keeper) QueryConsumerChain(goCtx context.Context, req *types.QueryConsumerChainRequest) (*types.QueryConsumerChainResponse, error) {
	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}

	consumerId := req.ConsumerId
	if err := ccvtypes.ValidateConsumerId(consumerId); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	chainId, err := k.GetConsumerChainId(ctx, consumerId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "cannot retrieve chain id for consumer id: %s", consumerId)
	}

	ownerAddress, err := k.GetConsumerOwnerAddress(ctx, consumerId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "cannot retrieve owner address for consumer id: %s", consumerId)
	}

	phase := k.GetConsumerPhase(ctx, consumerId)
	if phase == types.CONSUMER_PHASE_UNSPECIFIED {
		return nil, status.Errorf(codes.InvalidArgument, "cannot retrieve phase for consumer id: %s", consumerId)
	}

	metadata, err := k.GetConsumerMetadata(ctx, consumerId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "cannot retrieve metadata for consumer id: %s", consumerId)
	}

	initParams, _ := k.GetConsumerInitializationParameters(ctx, consumerId)
	infractionParams, err := types.DefaultConsumerInfractionParameters(ctx, k.slashingKeeper)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "cannot retrieve default infraction parameters: %s", err)
	}

	clientId, _ := k.GetConsumerClientId(ctx, consumerId)

	return &types.QueryConsumerChainResponse{
		ChainId:              chainId,
		ConsumerId:           consumerId,
		OwnerAddress:         ownerAddress,
		Phase:                phase.String(),
		Metadata:             metadata,
		InitParams:           &initParams,
		InfractionParameters: &infractionParams,
		ClientId:             clientId,
	}, nil
}

func (k Keeper) QueryConsumerGenesisTime(goCtx context.Context, req *types.QueryConsumerGenesisTimeRequest) (*types.QueryConsumerGenesisTimeResponse, error) {
	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}

	consumerId := req.ConsumerId
	if err := ccvtypes.ValidateConsumerId(consumerId); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	params, err := k.GetConsumerInitializationParameters(ctx, consumerId)
	if err != nil {
		return nil, status.Errorf(
			codes.InvalidArgument,
			"cannot get consumer genesis time for consumer Id: %s: %s",
			consumerId, types.ErrUnknownConsumerId,
		)
	}

	clientID, ok := k.GetConsumerClientId(ctx, consumerId)
	if !ok {
		return nil, status.Errorf(
			codes.InvalidArgument,
			"cannot get consumer genesis time for consumer Id: %s: consumer hasn't been launched or has been stopped and deleted",
			consumerId,
		)
	}

	cs, ok := k.clientKeeper.GetClientConsensusState(
		ctx,
		clientID,
		params.InitialHeight,
	)
	if !ok {
		return nil, status.Errorf(
			codes.InvalidArgument,
			"cannot get consumer genesis time for consumer Id: %s: cannot find consensus state for initial height: %s",
			consumerId,
			params.InitialHeight,
		)
	}

	return &types.QueryConsumerGenesisTimeResponse{
		GenesisTime: time.Unix(0, int64(cs.GetTimestamp())), // nolint:staticcheck
	}, nil
}
