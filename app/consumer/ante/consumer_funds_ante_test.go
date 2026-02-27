package ante

import (
	"bytes"
	"context"
	"testing"

	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
	protov2 "google.golang.org/protobuf/proto"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"
)

type mockConsumerFundsKeeper struct {
	providerChannelFound bool
	feeCollectorAddr     sdk.AccAddress
	feeCollectorFound    bool
	feeCollectorFunded   bool
}

func (m mockConsumerFundsKeeper) GetProviderChannel(context.Context) (string, bool) {
	return "channel-0", m.providerChannelFound
}

func (m mockConsumerFundsKeeper) GetFeeCollectorAccountAddress(context.Context) (sdk.AccAddress, bool) {
	if !m.feeCollectorFound {
		return nil, false
	}
	return m.feeCollectorAddr, true
}

func (m mockConsumerFundsKeeper) HasFeeCollectorFunds(context.Context, []string) bool {
	return m.feeCollectorFunded
}

type mockTx struct {
	msgs []sdk.Msg
}

func (m mockTx) GetMsgs() []sdk.Msg {
	return m.msgs
}

func (m mockTx) GetMsgsV2() ([]protov2.Message, error) {
	return nil, nil
}

func TestConsumerFundsDecoratorSkipsGateBeforeProviderChannel(t *testing.T) {
	collector := testAccAddress(1)
	msg := &banktypes.MsgSend{
		FromAddress: testAccAddress(2).String(),
		ToAddress:   testAccAddress(3).String(),
		Amount:      sdk.NewCoins(sdk.NewInt64Coin("uatone", 10)),
	}

	ctx := sdk.Context{}.WithMinGasPrices(sdk.NewDecCoinsFromCoins(sdk.NewInt64Coin("uatone", 1)))
	decorator := NewConsumerFundsDecorator(mockConsumerFundsKeeper{
		providerChannelFound: false,
		feeCollectorAddr:     collector,
		feeCollectorFound:    true,
		feeCollectorFunded:   false,
	})

	nextCalled := false
	_, err := decorator.AnteHandle(ctx, mockTx{msgs: []sdk.Msg{msg}}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	})
	require.NoError(t, err)
	require.True(t, nextCalled)
}

func TestConsumerFundsDecoratorBlocksNonTopUpTxWhenUnderfunded(t *testing.T) {
	collector := testAccAddress(1)
	msg := &banktypes.MsgSend{
		FromAddress: testAccAddress(2).String(),
		ToAddress:   testAccAddress(3).String(),
		Amount:      sdk.NewCoins(sdk.NewInt64Coin("uatone", 10)),
	}

	ctx := sdk.Context{}.WithMinGasPrices(sdk.NewDecCoinsFromCoins(sdk.NewInt64Coin("uatone", 1)))
	decorator := NewConsumerFundsDecorator(mockConsumerFundsKeeper{
		providerChannelFound: true,
		feeCollectorAddr:     collector,
		feeCollectorFound:    true,
		feeCollectorFunded:   false,
	})

	nextCalled := false
	_, err := decorator.AnteHandle(ctx, mockTx{msgs: []sdk.Msg{msg}}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	})
	require.Error(t, err)
	require.True(t, errorsmod.IsOf(err, consumertypes.ErrConsumerAccountUnderfunded))
	require.False(t, nextCalled)
}

func TestConsumerFundsDecoratorAllowsTopUpTxWhenUnderfunded(t *testing.T) {
	collector := testAccAddress(1)
	msg := &banktypes.MsgSend{
		FromAddress: testAccAddress(2).String(),
		ToAddress:   collector.String(),
		Amount:      sdk.NewCoins(sdk.NewInt64Coin("uatone", 10)),
	}

	ctx := sdk.Context{}.WithMinGasPrices(sdk.NewDecCoinsFromCoins(sdk.NewInt64Coin("uatone", 1)))
	decorator := NewConsumerFundsDecorator(mockConsumerFundsKeeper{
		providerChannelFound: true,
		feeCollectorAddr:     collector,
		feeCollectorFound:    true,
		feeCollectorFunded:   false,
	})

	nextCalled := false
	_, err := decorator.AnteHandle(ctx, mockTx{msgs: []sdk.Msg{msg}}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	})
	require.NoError(t, err)
	require.True(t, nextCalled)
}

func TestConsumerFundsDecoratorAllowsTxWhenFunded(t *testing.T) {
	collector := testAccAddress(1)
	msg := &banktypes.MsgSend{
		FromAddress: testAccAddress(2).String(),
		ToAddress:   testAccAddress(3).String(),
		Amount:      sdk.NewCoins(sdk.NewInt64Coin("uatone", 10)),
	}

	ctx := sdk.Context{}.WithMinGasPrices(sdk.NewDecCoinsFromCoins(sdk.NewInt64Coin("uatone", 1)))
	decorator := NewConsumerFundsDecorator(mockConsumerFundsKeeper{
		providerChannelFound: true,
		feeCollectorAddr:     collector,
		feeCollectorFound:    true,
		feeCollectorFunded:   true,
	})

	nextCalled := false
	_, err := decorator.AnteHandle(ctx, mockTx{msgs: []sdk.Msg{msg}}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	})
	require.NoError(t, err)
	require.True(t, nextCalled)
}

func TestConsumerFundsDecoratorBlocksWhenFeeCollectorMissing(t *testing.T) {
	msg := &banktypes.MsgSend{
		FromAddress: testAccAddress(2).String(),
		ToAddress:   testAccAddress(3).String(),
		Amount:      sdk.NewCoins(sdk.NewInt64Coin("uatone", 10)),
	}

	ctx := sdk.Context{}.WithMinGasPrices(sdk.NewDecCoinsFromCoins(sdk.NewInt64Coin("uatone", 1)))
	decorator := NewConsumerFundsDecorator(mockConsumerFundsKeeper{
		providerChannelFound: true,
		feeCollectorFound:    false,
		feeCollectorFunded:   false,
	})

	nextCalled := false
	_, err := decorator.AnteHandle(ctx, mockTx{msgs: []sdk.Msg{msg}}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	})
	require.Error(t, err)
	require.True(t, errorsmod.IsOf(err, consumertypes.ErrConsumerAccountUnderfunded))
	require.False(t, nextCalled)
}

func testAccAddress(seed byte) sdk.AccAddress {
	return sdk.AccAddress(bytes.Repeat([]byte{seed}, 20))
}
