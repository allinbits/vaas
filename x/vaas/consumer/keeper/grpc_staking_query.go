package keeper

import (
	"context"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// StakingQueryServer implements the staking query server interface
// to allow Hermes and other IBC relayers to query staking params
type StakingQueryServer struct {
	keeper *Keeper
}

// NewStakingQueryServer creates a new StakingQueryServer
func NewStakingQueryServer(k *Keeper) StakingQueryServer {
	return StakingQueryServer{keeper: k}
}

// Params returns staking params - required by Hermes for IBC client creation
func (s StakingQueryServer) Params(goCtx context.Context, req *stakingtypes.QueryParamsRequest) (*stakingtypes.QueryParamsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	params := s.keeper.GetConsumerParams(ctx)

	// Return staking-compatible params with unbonding period from consumer params
	stakingParams := stakingtypes.Params{
		UnbondingTime:     params.UnbondingPeriod,
		MaxValidators:     100,
		MaxEntries:        7,
		HistoricalEntries: uint32(params.HistoricalEntries),
		BondDenom:         "uatone",
		MinCommissionRate: math.LegacyZeroDec(),
	}

	return &stakingtypes.QueryParamsResponse{Params: stakingParams}, nil
}

// Validators is not supported on consumer chains
func (s StakingQueryServer) Validators(context.Context, *stakingtypes.QueryValidatorsRequest) (*stakingtypes.QueryValidatorsResponse, error) {
	return &stakingtypes.QueryValidatorsResponse{}, nil
}

// Validator is not supported on consumer chains
func (s StakingQueryServer) Validator(context.Context, *stakingtypes.QueryValidatorRequest) (*stakingtypes.QueryValidatorResponse, error) {
	return &stakingtypes.QueryValidatorResponse{}, nil
}

// ValidatorDelegations is not supported on consumer chains
func (s StakingQueryServer) ValidatorDelegations(context.Context, *stakingtypes.QueryValidatorDelegationsRequest) (*stakingtypes.QueryValidatorDelegationsResponse, error) {
	return &stakingtypes.QueryValidatorDelegationsResponse{}, nil
}

// ValidatorUnbondingDelegations is not supported on consumer chains
func (s StakingQueryServer) ValidatorUnbondingDelegations(context.Context, *stakingtypes.QueryValidatorUnbondingDelegationsRequest) (*stakingtypes.QueryValidatorUnbondingDelegationsResponse, error) {
	return &stakingtypes.QueryValidatorUnbondingDelegationsResponse{}, nil
}

// Delegation is not supported on consumer chains
func (s StakingQueryServer) Delegation(context.Context, *stakingtypes.QueryDelegationRequest) (*stakingtypes.QueryDelegationResponse, error) {
	return &stakingtypes.QueryDelegationResponse{}, nil
}

// UnbondingDelegation is not supported on consumer chains
func (s StakingQueryServer) UnbondingDelegation(context.Context, *stakingtypes.QueryUnbondingDelegationRequest) (*stakingtypes.QueryUnbondingDelegationResponse, error) {
	return &stakingtypes.QueryUnbondingDelegationResponse{}, nil
}

// DelegatorDelegations is not supported on consumer chains
func (s StakingQueryServer) DelegatorDelegations(context.Context, *stakingtypes.QueryDelegatorDelegationsRequest) (*stakingtypes.QueryDelegatorDelegationsResponse, error) {
	return &stakingtypes.QueryDelegatorDelegationsResponse{}, nil
}

// DelegatorUnbondingDelegations is not supported on consumer chains
func (s StakingQueryServer) DelegatorUnbondingDelegations(context.Context, *stakingtypes.QueryDelegatorUnbondingDelegationsRequest) (*stakingtypes.QueryDelegatorUnbondingDelegationsResponse, error) {
	return &stakingtypes.QueryDelegatorUnbondingDelegationsResponse{}, nil
}

// Redelegations is not supported on consumer chains
func (s StakingQueryServer) Redelegations(context.Context, *stakingtypes.QueryRedelegationsRequest) (*stakingtypes.QueryRedelegationsResponse, error) {
	return &stakingtypes.QueryRedelegationsResponse{}, nil
}

// DelegatorValidators is not supported on consumer chains
func (s StakingQueryServer) DelegatorValidators(context.Context, *stakingtypes.QueryDelegatorValidatorsRequest) (*stakingtypes.QueryDelegatorValidatorsResponse, error) {
	return &stakingtypes.QueryDelegatorValidatorsResponse{}, nil
}

// DelegatorValidator is not supported on consumer chains
func (s StakingQueryServer) DelegatorValidator(context.Context, *stakingtypes.QueryDelegatorValidatorRequest) (*stakingtypes.QueryDelegatorValidatorResponse, error) {
	return &stakingtypes.QueryDelegatorValidatorResponse{}, nil
}

// HistoricalInfo is not supported on consumer chains
func (s StakingQueryServer) HistoricalInfo(context.Context, *stakingtypes.QueryHistoricalInfoRequest) (*stakingtypes.QueryHistoricalInfoResponse, error) {
	return &stakingtypes.QueryHistoricalInfoResponse{}, nil
}

// Pool is not supported on consumer chains
func (s StakingQueryServer) Pool(context.Context, *stakingtypes.QueryPoolRequest) (*stakingtypes.QueryPoolResponse, error) {
	return &stakingtypes.QueryPoolResponse{
		Pool: stakingtypes.Pool{},
	}, nil
}
