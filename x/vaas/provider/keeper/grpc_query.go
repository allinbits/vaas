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
	"cosmossdk.io/math"

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

	return types.Chain{
		ChainId:        chainID,
		ClientId:       clientID,
		Phase:          k.GetConsumerPhase(ctx, consumerId).String(),
		Metadata:       metadata,
		ConsumerId:     consumerId,
		FeePoolAddress: k.GetConsumerFeePoolAddress(consumerId).String(),
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

	clientId, _ := k.GetConsumerClientId(ctx, consumerId)

	return &types.QueryConsumerChainResponse{
		ChainId:        chainId,
		ConsumerId:     consumerId,
		OwnerAddress:   ownerAddress,
		Phase:          phase.String(),
		Metadata:       metadata,
		InitParams:     &initParams,
		ClientId:       clientId,
		FeePoolAddress: k.GetConsumerFeePoolAddress(consumerId).String(),
	}, nil
}

func (k Keeper) ConsumerFeePoolClaim(
	goCtx context.Context, req *types.QueryConsumerFeePoolClaimRequest,
) (*types.QueryConsumerFeePoolClaimResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	exists, err := k.ConsumerPhase.Has(ctx, req.ConsumerId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "phase lookup: %s", err)
	}
	if !exists {
		return nil, status.Errorf(codes.NotFound, "consumer %d does not exist", req.ConsumerId)
	}
	if k.GetConsumerPhase(ctx, req.ConsumerId) == types.CONSUMER_PHASE_DELETED {
		return nil, status.Errorf(codes.NotFound, "consumer %d is deleted", req.ConsumerId)
	}

	depositor, err := sdk.AccAddressFromBech32(req.Depositor)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid depositor: %s", err)
	}
	if k.IsAuthority(req.Depositor) {
		depositor = authtypes.NewModuleAddress(disttypes.ModuleName)
	}

	poolAddr := k.GetConsumerFeePoolAddress(req.ConsumerId)
	coins := sdk.NewCoins()
	prefix := collections.NewPrefixedPairRange[uint64, string](req.ConsumerId)
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
		total, err := iter.Value()
		if err != nil {
			return nil, status.Errorf(codes.Internal, "value: %s", err)
		}
		denom := key.K2()
		if total.IsZero() {
			continue
		}
		shares, err := k.ConsumerFeePoolShares.Get(ctx,
			collections.Join3(req.ConsumerId, denom, depositor))
		if err != nil {
			// No shares for this (consumer, depositor, denom) — skip.
			continue
		}
		balance := k.bankKeeper.GetBalance(ctx, poolAddr, denom)
		if balance.Amount.IsZero() {
			continue
		}
		claim := shares.Mul(balance.Amount).Quo(total)
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

	exists, err := k.ConsumerPhase.Has(ctx, req.ConsumerId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "phase lookup: %s", err)
	}
	if !exists {
		return nil, status.Errorf(codes.NotFound, "consumer %d does not exist", req.ConsumerId)
	}
	if k.GetConsumerPhase(ctx, req.ConsumerId) == types.CONSUMER_PHASE_DELETED {
		return nil, status.Errorf(codes.NotFound, "consumer %d is deleted", req.ConsumerId)
	}

	type acc struct {
		addr  sdk.AccAddress
		coins sdk.Coins
	}
	perDepositor := map[string]*acc{}

	// Cache per-denom (balance, total) so we don't pay one bank read +
	// one collection read per (depositor, denom) tuple.
	poolAddr := k.GetConsumerFeePoolAddress(req.ConsumerId)
	balances := map[string]math.Int{}
	totals := map[string]math.Int{}

	prefix := collections.NewPrefixedTripleRange[uint64, string, sdk.AccAddress](req.ConsumerId)
	iter, err := k.ConsumerFeePoolShares.Iterate(ctx, prefix)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%s", err)
	}
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			iter.Close()
			return nil, status.Errorf(codes.Internal, "%s", err)
		}
		shares, err := iter.Value()
		if err != nil {
			iter.Close()
			return nil, status.Errorf(codes.Internal, "%s", err)
		}
		denom := key.K2()
		addr := key.K3()

		balance, ok := balances[denom]
		if !ok {
			balance = k.bankKeeper.GetBalance(ctx, poolAddr, denom).Amount
			balances[denom] = balance
		}
		if balance.IsZero() {
			continue
		}
		total, ok := totals[denom]
		if !ok {
			t, err := k.ConsumerFeePoolTotalShares.Get(ctx,
				collections.Join(req.ConsumerId, denom))
			if err != nil {
				continue
			}
			total = t
			totals[denom] = total
		}
		if total.IsZero() {
			continue
		}
		claim := shares.Mul(balance).Quo(total)
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

	total := uint64(len(keys))

	// Decode pagination request. Key takes precedence over Offset: cosmos
	// paging clients walk a result set with NextKey, so a non-nil Key means
	// "resume from this depositor".
	var offset, limit uint64 = 0, query.DefaultLimit
	countTotal := true
	if req.Pagination != nil {
		if req.Pagination.Limit > 0 {
			limit = req.Pagination.Limit
		}
		countTotal = req.Pagination.CountTotal
		if len(req.Pagination.Key) > 0 {
			// Find the first depositor >= Key.
			cursor := string(req.Pagination.Key)
			idx := sort.SearchStrings(keys, cursor)
			offset = uint64(idx)
		} else {
			offset = req.Pagination.Offset
		}
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}

	claims := make([]types.DepositorClaim, 0, end-offset)
	for _, key := range keys[offset:end] {
		entry := perDepositor[key]
		claims = append(claims, types.DepositorClaim{
			Depositor: entry.addr.String(),
			Claim:     entry.coins,
		})
	}

	resp := &types.QueryConsumerFeePoolClaimsResponse{
		Claims:     claims,
		Pagination: &query.PageResponse{},
	}
	if countTotal {
		resp.Pagination.Total = total
	}
	if end < total {
		// NextKey is the bech32 depositor at the boundary, encoded as bytes
		// for the PageResponse contract. Resume decodes it back via
		// string(req.Pagination.Key) and binary-searches the same sort order.
		resp.Pagination.NextKey = []byte(keys[end])
	}
	return resp, nil
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

// QueryConsumerFeesPerBlock returns the effective per-block fee for the
// given consumer (override if set, else the default Params.FeesPerBlockAmount).
func (k Keeper) QueryConsumerFeesPerBlock(
	goCtx context.Context,
	req *types.QueryConsumerFeesPerBlockRequest,
) (*types.QueryConsumerFeesPerBlockResponse, error) {
	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}
	// Reject unknown and deleted consumers rather than reporting the default
	// fee for an id that does not (or no longer) exists.
	phase := k.GetConsumerPhase(goCtx, req.ConsumerId)
	if phase == types.CONSUMER_PHASE_UNSPECIFIED || phase == types.CONSUMER_PHASE_DELETED {
		return nil, status.Errorf(codes.NotFound, "consumer %d not found", req.ConsumerId)
	}
	coin, isOverride := k.GetEffectiveFeesPerBlock(goCtx, req.ConsumerId)
	return &types.QueryConsumerFeesPerBlockResponse{
		FeesPerBlock: coin,
		IsOverride:   isOverride,
	}, nil
}

func (k Keeper) QueryConsumerLiveness(goCtx context.Context, req *types.QueryConsumerLivenessRequest) (*types.QueryConsumerLivenessResponse, error) {
	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	if k.GetConsumerPhase(ctx, req.ConsumerId) == types.CONSUMER_PHASE_UNSPECIFIED {
		return nil, status.Errorf(codes.InvalidArgument, "unknown consumer: %d", req.ConsumerId)
	}

	grace, err := k.LivenessGracePeriod(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	lastAck := k.GetConsumerLastAckTime(ctx, req.ConsumerId)
	removalEta := lastAck.Add(grace)

	return &types.QueryConsumerLivenessResponse{
		LastAckTime: lastAck,
		GracePeriod: grace,
		RemovalEta:  removalEta,
		Degraded:    ctx.BlockTime().After(lastAck.Add(grace / 2)),
	}, nil
}

// QueryPendingDowntimeSlashes returns the pending downtime slashes queued for
// a consumer, awaiting the challenge window before execution. A validator can
// have more than one entry, one per accepted disjoint window; the result is
// unbounded but small enough that no pagination is offered.
func (k Keeper) QueryPendingDowntimeSlashes(goCtx context.Context, req *types.QueryPendingDowntimeSlashesRequest) (*types.QueryPendingDowntimeSlashesResponse, error) {
	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	if k.GetConsumerPhase(ctx, req.ConsumerId) == types.CONSUMER_PHASE_UNSPECIFIED {
		return nil, status.Errorf(codes.InvalidArgument, "unknown consumer: %d", req.ConsumerId)
	}

	prefix := collections.NewPrefixedTripleRange[uint64, []byte, int64](req.ConsumerId)
	iter, err := k.PendingDowntimeSlashes.Iterate(ctx, prefix)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "iterate pending downtime slashes: %s", err)
	}
	defer iter.Close()

	slashes := []types.PendingDowntimeSlash{}
	for ; iter.Valid(); iter.Next() {
		entry, err := iter.Value()
		if err != nil {
			return nil, status.Errorf(codes.Internal, "pending downtime slash value: %s", err)
		}
		slashes = append(slashes, entry)
	}

	return &types.QueryPendingDowntimeSlashesResponse{Slashes: slashes}, nil
}

// QueryWithheldFeeRecords returns the fee shares currently withheld from
// validators for a consumer due to a pending or executed downtime slash.
// There is at most one entry per validator, so the result is unbounded but
// small enough that no pagination is offered.
func (k Keeper) QueryWithheldFeeRecords(goCtx context.Context, req *types.QueryWithheldFeeRecordsRequest) (*types.QueryWithheldFeeRecordsResponse, error) {
	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	if k.GetConsumerPhase(ctx, req.ConsumerId) == types.CONSUMER_PHASE_UNSPECIFIED {
		return nil, status.Errorf(codes.InvalidArgument, "unknown consumer: %d", req.ConsumerId)
	}

	prefix := collections.NewPrefixedPairRange[uint64, []byte](req.ConsumerId)
	iter, err := k.WithheldFeeRecords.Iterate(ctx, prefix)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "iterate withheld fee records: %s", err)
	}
	defer iter.Close()

	records := []types.WithheldFeeRecord{}
	for ; iter.Valid(); iter.Next() {
		entry, err := iter.Value()
		if err != nil {
			return nil, status.Errorf(codes.Internal, "withheld fee record value: %s", err)
		}
		records = append(records, entry)
	}

	return &types.QueryWithheldFeeRecordsResponse{Records: records}, nil
}

// QueryAllConsumerFeesPerBlockOverrides returns the full list of overrides,
// paginated, ordered by consumer_id ascending.
func (k Keeper) QueryAllConsumerFeesPerBlockOverrides(
	goCtx context.Context,
	req *types.QueryAllConsumerFeesPerBlockOverridesRequest,
) (*types.QueryAllConsumerFeesPerBlockOverridesResponse, error) {
	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}
	overrides, pageRes, err := query.CollectionPaginate(
		goCtx,
		k.ConsumerFeesPerBlockOverride,
		req.Pagination,
		func(consumerId uint64, amt math.Int) (types.ConsumerFeesPerBlockOverride, error) {
			return types.ConsumerFeesPerBlockOverride{
				ConsumerId: consumerId,
				Amount:     amt.String(),
			}, nil
		},
	)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryAllConsumerFeesPerBlockOverridesResponse{
		Overrides:  overrides,
		Pagination: pageRes,
	}, nil
}
