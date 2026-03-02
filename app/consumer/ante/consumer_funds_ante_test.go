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
	balances             map[string]int64
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

func (m mockConsumerFundsKeeper) HasFeeCollectorFundsForAmount(_ context.Context, requiredAmount sdk.Coins) bool {
	for _, coin := range requiredAmount {
		if balance, ok := m.balances[coin.Denom]; ok && balance >= coin.Amount.Int64() {
			return true
		}
	}
	return false
}

type mockTx struct {
	msgs       []sdk.Msg
	fee        sdk.Coins
	gas        uint64
	feePayer   []byte
	feeGranter []byte
}

func (m mockTx) GetMsgs() []sdk.Msg {
	return m.msgs
}

func (m mockTx) GetMsgsV2() ([]protov2.Message, error) {
	return nil, nil
}

func (m mockTx) GetGas() uint64 {
	return m.gas
}

func (m mockTx) GetFee() sdk.Coins {
	return m.fee
}

func (m mockTx) FeePayer() []byte {
	return m.feePayer
}

func (m mockTx) FeeGranter() []byte {
	return m.feeGranter
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
		balances:             map[string]int64{},
	})

	nextCalled := false
	_, err := decorator.AnteHandle(ctx, mockTx{
		msgs: []sdk.Msg{msg},
		fee:  sdk.NewCoins(sdk.NewInt64Coin("uatone", 10)),
		gas:  100,
	}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
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
		balances: map[string]int64{
			"uatone": 9,
		},
	})

	nextCalled := false
	_, err := decorator.AnteHandle(ctx, mockTx{
		msgs: []sdk.Msg{msg},
		fee:  sdk.NewCoins(sdk.NewInt64Coin("uatone", 10)),
		gas:  100,
	}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
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
		balances:             map[string]int64{},
	})

	nextCalled := false
	_, err := decorator.AnteHandle(ctx, mockTx{
		msgs: []sdk.Msg{msg},
		fee:  sdk.NewCoins(sdk.NewInt64Coin("uatone", 10)),
		gas:  100,
	}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	})
	require.NoError(t, err)
	require.True(t, nextCalled)
}

func TestConsumerFundsDecoratorAllowsTxWhenExactlyFunded(t *testing.T) {
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
		balances: map[string]int64{
			"uatone": 10,
		},
	})

	nextCalled := false
	_, err := decorator.AnteHandle(ctx, mockTx{
		msgs: []sdk.Msg{msg},
		fee:  sdk.NewCoins(sdk.NewInt64Coin("uatone", 10)),
		gas:  100,
	}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
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
		balances:             map[string]int64{},
	})

	nextCalled := false
	_, err := decorator.AnteHandle(ctx, mockTx{
		msgs: []sdk.Msg{msg},
		fee:  sdk.NewCoins(sdk.NewInt64Coin("uatone", 10)),
		gas:  100,
	}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
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
