package keeper

import (
	"context"

	"github.com/allinbits/vaas/x/vaas/consumer/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	sdk "github.com/cosmos/cosmos-sdk/types"
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

// QueryProviderInfo returns provider information.
//
// IBC v2 Note: This query supports both IBC v1 (channel-based) and IBC v2 (client-based)
// routing. It first attempts to use v2 client-based info, then falls back to v1
// channel-based info for backward compatibility.
func (k Keeper) QueryProviderInfo(c context.Context, //nolint:golint
	req *types.QueryProviderInfoRequest,
) (*types.QueryProviderInfoResponse, error) {
	ctx := sdk.UnwrapSDKContext(c)
	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}

	// Try IBC v2 (client-based) first
	if resp, err := k.GetProviderInfoV2(ctx); err == nil {
		return resp, nil
	}

	// Fall back to IBC v1 (channel-based) for backward compatibility
	return k.GetProviderInfo(ctx)
}
