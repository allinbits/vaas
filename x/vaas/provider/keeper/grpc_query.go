package keeper

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	disttypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
)

var _ types.QueryServer = Keeper{}

func (k Keeper) QueryConsumerGenesis(c context.Context, req *types.QueryConsumerGenesisRequest) (*types.QueryConsumerGenesisResponse, error) {
	ctx := sdk.UnwrapSDKContext(c)

	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}

	consumerId := req.ConsumerId
	gen, ok := k.GetConsumerGenesis(ctx, consumerId)
	if !ok {
		return nil, status.Error(
			codes.NotFound,
			errorsmod.Wrapf(types.ErrUnknownConsumerId, "%d", consumerId).Error(),
		)
	}

	return &types.QueryConsumerGenesisResponse{GenesisState: gen}, nil
}

func (k Keeper) QueryConsumerChains(goCtx context.Context, req *types.QueryConsumerChainsRequest) (*types.QueryConsumerChainsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	chains, pageRes, err := query.CollectionFilteredPaginate(
		ctx,
		k.ConsumerPhase,
		req.Pagination,
		func(consumerId uint64, phaseValue uint32) (bool, error) {
			// Filter by phase if specified
			if req.Phase != types.CONSUMER_PHASE_UNSPECIFIED {
				return types.ConsumerPhase(phaseValue) == req.Phase, nil
			}
			return true, nil
		},
		func(consumerId uint64, phaseValue uint32) (*types.Chain, error) {
			c, err := k.GetConsumerChain(ctx, consumerId)
			if err != nil {
				return nil, err
			}
			return &c, nil
		},
	)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryConsumerChainsResponse{Chains: chains, Pagination: pageRes}, nil
}

// GetConsumerChain returns a Chain data structure with all the necessary fields
func (k Keeper) GetConsumerChain(ctx sdk.Context, consumerId uint64) (types.Chain, error) {
	chainID, err := k.GetConsumerChainId(ctx, consumerId)
	if err != nil {
		return types.Chain{}, fmt.Errorf("cannot find chainID for consumer (%d)", consumerId)
	}

	clientID, _ := k.GetConsumerClientId(ctx, consumerId)

	metadata, err := k.GetConsumerMetadata(ctx, consumerId)
	if err != nil {
		return types.Chain{}, fmt.Errorf("cannot find metadata (%d): %s", consumerId, err.Error())
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
		FeePoolAddress:       k.GetConsumerFeePoolAddress(consumerId).String(),
	}, nil
}

func (k Keeper) QueryValidatorConsumerAddr(goCtx context.Context, req *types.QueryValidatorConsumerAddrRequest) (*types.QueryValidatorConsumerAddrResponse, error) {
	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	consumerId := req.ConsumerId
	providerAddrTmp, err := sdk.ConsAddressFromBech32(req.ProviderAddress)
	if err != nil {
		return nil, err
	}
	providerAddr := types.NewProviderConsAddress(providerAddrTmp)

	consumerKey, found := k.GetValidatorConsumerPubKey(ctx, consumerId, providerAddr)
	if !found {
		return &types.QueryValidatorConsumerAddrResponse{}, nil
	}

	consumerAddr, err := vaastypes.TMCryptoPublicKeyToConsAddr(consumerKey)
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
	pairValConAddrs := []*types.PairValConAddrProviderAndConsumer{}

	ctx := sdk.UnwrapSDKContext(goCtx)
	validatorConsumerPubKeys := k.GetAllValidatorConsumerPubKeys(ctx, &consumerId)
	for _, data := range validatorConsumerPubKeys {
		consumerAddr, err := vaastypes.TMCryptoPublicKeyToConsAddr(*data.ConsumerKey)
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
	ctx := sdk.UnwrapSDKContext(goCtx)

	phase := k.GetConsumerPhase(ctx, consumerId)
	if phase == types.CONSUMER_PHASE_UNSPECIFIED {
		return nil, status.Errorf(codes.InvalidArgument, "cannot find a phase for consumer: %d", consumerId)
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

		validators = append(validators, &types.QueryConsumerValidatorsValidator{
			ProviderAddress:         sdk.ConsAddress(consumerVal.ProviderConsAddr).String(),
			ConsumerKey:             consumerVal.PublicKey,
			ConsumerPower:           consumerVal.Power,
			Description:             providerVal.Description,
			ProviderOperatorAddress: providerVal.OperatorAddress,
			Jailed:                  providerVal.Jailed,
			Status:                  providerVal.Status,
			ProviderTokens:          providerVal.Tokens,
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
	ctx := sdk.UnwrapSDKContext(goCtx)

	chainId, err := k.GetConsumerChainId(ctx, consumerId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "cannot retrieve chain id for consumer id: %d", consumerId)
	}

	ownerAddress, err := k.GetConsumerOwnerAddress(ctx, consumerId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "cannot retrieve owner address for consumer id: %d", consumerId)
	}

	phase := k.GetConsumerPhase(ctx, consumerId)
	if phase == types.CONSUMER_PHASE_UNSPECIFIED {
		return nil, status.Errorf(codes.InvalidArgument, "cannot retrieve phase for consumer id: %d", consumerId)
	}

	metadata, err := k.GetConsumerMetadata(ctx, consumerId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "cannot retrieve metadata for consumer id: %d", consumerId)
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
		FeePoolAddress:       k.GetConsumerFeePoolAddress(consumerId).String(),
	}, nil
}

func (k Keeper) ConsumerFeePoolClaim(
	goCtx context.Context, req *types.QueryConsumerFeePoolClaimRequest,
) (*types.QueryConsumerFeePoolClaimResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	depositorBech := req.Depositor
	if depositorBech == k.GetAuthority() {
		depositorBech = authtypes.NewModuleAddress(disttypes.ModuleName).String()
	}
	depositor, err := sdk.AccAddressFromBech32(depositorBech)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid depositor: %s", err)
	}

	coins := sdk.NewCoins()
	prefix := collections.NewPrefixedPairRange[string, string](req.ConsumerId)
	iter, err := k.ConsumerFeePoolTotalShares.Iterate(ctx, prefix)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "iterate totals: %s", err)
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			return nil, status.Errorf(codes.Internal, "key: %s", err)
		}
		denom := key.K2()
		claim := k.ComputeClaim(ctx, req.ConsumerId, depositor, denom)
		if claim.IsPositive() {
			coins = coins.Add(sdk.NewCoin(denom, claim))
		}
	}
	return &types.QueryConsumerFeePoolClaimResponse{Claim: coins}, nil
}

func (k Keeper) ConsumerFeePoolClaims(
	goCtx context.Context, req *types.QueryConsumerFeePoolClaimsRequest,
) (*types.QueryConsumerFeePoolClaimsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	type acc struct {
		addr  sdk.AccAddress
		coins sdk.Coins
	}
	perDepositor := map[string]*acc{}

	prefix := collections.NewPrefixedTripleRange[string, sdk.AccAddress, string](req.ConsumerId)
	iter, err := k.ConsumerFeePoolShares.Iterate(ctx, prefix)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%s", err)
	}
	for ; iter.Valid(); iter.Next() {
		key, _ := iter.Key()
		addr := key.K2()
		denom := key.K3()
		claim := k.ComputeClaim(ctx, req.ConsumerId, addr, denom)
		if claim.IsZero() {
			continue
		}
		if entry, ok := perDepositor[addr.String()]; ok {
			entry.coins = entry.coins.Add(sdk.NewCoin(denom, claim))
		} else {
			perDepositor[addr.String()] = &acc{
				addr:  addr,
				coins: sdk.NewCoins(sdk.NewCoin(denom, claim)),
			}
		}
	}
	iter.Close()

	keys := make([]string, 0, len(perDepositor))
	for key := range perDepositor {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	offset, limit := uint64(0), uint64(100)
	if req.Pagination != nil {
		offset = req.Pagination.Offset
		if req.Pagination.Limit > 0 {
			limit = req.Pagination.Limit
		}
	}

	end := offset + limit
	if end > uint64(len(keys)) {
		end = uint64(len(keys))
	}
	if offset > uint64(len(keys)) {
		offset = uint64(len(keys))
	}

	claims := make([]types.DepositorClaim, 0, end-offset)
	for _, key := range keys[offset:end] {
		entry := perDepositor[key]
		claims = append(claims, types.DepositorClaim{
			Depositor: entry.addr.String(),
			Claim:     entry.coins,
		})
	}
	return &types.QueryConsumerFeePoolClaimsResponse{
		Claims: claims,
		Pagination: &query.PageResponse{
			Total: uint64(len(keys)),
		},
	}, nil
}

func (k Keeper) QueryConsumerGenesisTime(goCtx context.Context, req *types.QueryConsumerGenesisTimeRequest) (*types.QueryConsumerGenesisTimeResponse, error) {
	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}

	consumerId := req.ConsumerId
	ctx := sdk.UnwrapSDKContext(goCtx)

	params, err := k.GetConsumerInitializationParameters(ctx, consumerId)
	if err != nil {
		return nil, status.Errorf(
			codes.InvalidArgument,
			"cannot get consumer genesis time for consumer Id: %d: %s",
			consumerId, types.ErrUnknownConsumerId,
		)
	}

	clientID, ok := k.GetConsumerClientId(ctx, consumerId)
	if !ok {
		return nil, status.Errorf(
			codes.InvalidArgument,
			"cannot get consumer genesis time for consumer Id: %d: consumer hasn't been launched or has been stopped and deleted",
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
			"cannot get consumer genesis time for consumer Id: %d: cannot find consensus state for initial height: %s",
			consumerId,
			params.InitialHeight,
		)
	}

	return &types.QueryConsumerGenesisTimeResponse{
		GenesisTime: time.Unix(0, int64(cs.GetTimestamp())), //nolint:staticcheck
	}, nil
}
