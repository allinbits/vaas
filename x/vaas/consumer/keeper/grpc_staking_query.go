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

// Params returns the subset of staking params that have a meaningful value on a
// VAAS consumer chain. The endpoint exists so IBC relayers (e.g. Hermes,
// ts-relayer) can read UnbondingTime when deriving the trusting_period of a
// Tendermint light client targeting this chain.
//
// Consumer chains have no local validator selection, no local bonding, and no
// local commission — the active set is dictated by the provider via VSC
// packets. The remaining staking.Params fields (BondDenom, MaxValidators,
// MaxEntries, MinCommissionRate) are therefore left at their zero values
// rather than fabricated, so callers are not misled.
func (s StakingQueryServer) Params(goCtx context.Context, req *stakingtypes.QueryParamsRequest) (*stakingtypes.QueryParamsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	params := s.keeper.GetConsumerParams(ctx)

	stakingParams := stakingtypes.Params{
		UnbondingTime:     params.UnbondingPeriod,
		HistoricalEntries: uint32(params.HistoricalEntries),
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
