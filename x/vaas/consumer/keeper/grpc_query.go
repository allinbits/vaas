package keeper

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/allinbits/vaas/x/vaas/consumer/types"
)

var _ types.QueryServer = Keeper{} //nolint:golint

func (k Keeper) QueryParams(c context.Context, //nolint:golint
	req *types.QueryParamsRequest,
) (*types.QueryParamsResponse, error) {
	ctx := sdk.UnwrapSDKContext(c)

	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}

	p := k.GetConsumerParams(ctx)

	return &types.QueryParamsResponse{Params: p}, nil
}

// QueryOwnerAuthority returns the account the owner's MsgOwnerExec messages
// are signed by, under the chain's bech32 prefix, and the owner that may
// submit them.
func (k Keeper) QueryOwnerAuthority(c context.Context, //nolint:golint
	req *types.QueryOwnerAuthorityRequest,
) (*types.QueryOwnerAuthorityResponse, error) {
	ctx := sdk.UnwrapSDKContext(c)
	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}
	return &types.QueryOwnerAuthorityResponse{
		Authority: types.OwnerExecAuthority().String(),
		Owner:     k.GetConsumerParams(ctx).OwnerAddress,
	}, nil
}

// QueryProviderInfo returns provider information.
func (k Keeper) QueryProviderInfo(c context.Context, //nolint:golint
	req *types.QueryProviderInfoRequest,
) (*types.QueryProviderInfoResponse, error) {
	ctx := sdk.UnwrapSDKContext(c)
	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}

	resp, err := k.GetProviderInfoV2(ctx)
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return resp, nil
}
