package ante

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	protov2 "google.golang.org/protobuf/proto"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	channeltypes "github.com/cosmos/ibc-go/v10/modules/core/04-channel/types"
	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"

	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
)

type mockConsumerKeeper struct {
	providerClientFound bool
	inDebt              bool
	vscStale            bool
	routableClient      bool
	photonFees          bool
	// noVouchers models a chain whose bank supply of the photon voucher
	// denom is zero.
	noVouchers bool
}

func (m mockConsumerKeeper) GetProviderClientID(context.Context) (string, bool) {
	return "07-tendermint-0", m.providerClientFound
}

func (m mockConsumerKeeper) PhotonFeesEnabled(context.Context) bool {
	return m.photonFees
}

func (m mockConsumerKeeper) IsConsumerInDebt(context.Context) bool {
	return m.inDebt
}

func (m mockConsumerKeeper) IsVSCStale(context.Context) bool {
	return m.vscStale
}

func (m mockConsumerKeeper) HasRoutableProviderClient(context.Context) bool {
	return m.providerClientFound && m.routableClient
}

func (m mockConsumerKeeper) HasSupply(_ context.Context, denom string) bool {
	return denom == ExpectedPhotonDenom("07-tendermint-0") && !m.noVouchers
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

func runDecorator(t *testing.T, k mockConsumerKeeper, msgs []sdk.Msg) (bool, error) {
	t.Helper()
	decorator := NewMsgFilterDecorator(k)
	nextCalled := false
	_, err := decorator.AnteHandle(sdk.Context{}, mockTx{msgs: msgs}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	})
	return nextCalled, err
}

func bankSendMsg() sdk.Msg {
	return &banktypes.MsgSend{
		FromAddress: testAccAddress(1).String(),
		ToAddress:   testAccAddress(2).String(),
		Amount:      sdk.NewCoins(sdk.NewInt64Coin("uatone", 10)),
	}
}

// Bootstrap: /ibc.* messages are accepted so the IBC stack can be set up.
func TestMsgFilterDecoratorBootstrapAllowsIBCMsgs(t *testing.T) {
	nextCalled, err := runDecorator(t,
		mockConsumerKeeper{providerClientFound: false},
		[]sdk.Msg{&channeltypes.MsgRecvPacket{}},
	)
	require.NoError(t, err)
	require.True(t, nextCalled)
}

// Bootstrap: any non-/ibc.* message is rejected.
func TestMsgFilterDecoratorBootstrapRejectsNonIBCMsgs(t *testing.T) {
	nextCalled, err := runDecorator(t,
		mockConsumerKeeper{providerClientFound: false},
		[]sdk.Msg{bankSendMsg()},
	)
	require.Error(t, err)
	require.False(t, nextCalled)
}

// Provider client established, not in debt: everything passes.
func TestMsgFilterDecoratorAllowsNonIBCTxWhenNotInDebt(t *testing.T) {
	nextCalled, err := runDecorator(t,
		mockConsumerKeeper{providerClientFound: true, inDebt: false},
		[]sdk.Msg{bankSendMsg()},
	)
	require.NoError(t, err)
	require.True(t, nextCalled)
}

// Provider client established, in debt: non-IBC, non-gov msgs are rejected with ErrConsumerInDebt.
func TestMsgFilterDecoratorBlocksNonIBCTxWhenInDebt(t *testing.T) {
	nextCalled, err := runDecorator(t,
		mockConsumerKeeper{providerClientFound: true, inDebt: true},
		[]sdk.Msg{bankSendMsg()},
	)
	require.Error(t, err)
	require.True(t, errorsmod.IsOf(err, consumertypes.ErrConsumerInDebt))
	require.False(t, nextCalled)
}

// Provider client established, in debt: /ibc.core.* msgs pass to keep VAAS liveness.
func TestMsgFilterDecoratorAllowsIBCCoreTxWhenInDebt(t *testing.T) {
	nextCalled, err := runDecorator(t,
		mockConsumerKeeper{providerClientFound: true, inDebt: true},
		[]sdk.Msg{&channeltypes.MsgRecvPacket{}},
	)
	require.NoError(t, err)
	require.True(t, nextCalled)
}

// Provider client established, in debt: a tx mixing /ibc.core.* with anything else is rejected.
func TestMsgFilterDecoratorBlocksMixedIBCCoreAndNonIBCMessagesWhenInDebt(t *testing.T) {
	nextCalled, err := runDecorator(t,
		mockConsumerKeeper{providerClientFound: true, inDebt: true},
		[]sdk.Msg{&channeltypes.MsgRecvPacket{}, bankSendMsg()},
	)
	require.Error(t, err)
	require.True(t, errorsmod.IsOf(err, consumertypes.ErrConsumerInDebt))
	require.False(t, nextCalled)
}

// Governance messages are allowed during debt so the community can vote on
// recovery (emergency funding, param changes) without off-chain coordination.
func TestMsgFilterDecoratorAllowsGovTxWhenInDebt(t *testing.T) {
	msg := &govtypes.MsgVote{
		ProposalId: 1,
		Voter:      testAccAddress(1).String(),
		Option:     govtypes.OptionYes,
	}
	nextCalled, err := runDecorator(t,
		mockConsumerKeeper{providerClientFound: true, inDebt: true},
		[]sdk.Msg{msg},
	)
	require.NoError(t, err)
	require.True(t, nextCalled)
}

// Authz-wrapped IBC core txs are treated as non-IBC and rejected while the
// consumer is in debt. Real relayers sign /ibc.core.* messages directly with
// their own keys, so this path is not needed for VAAS liveness.
func TestMsgFilterDecoratorRejectsAuthzWrappedIBCCoreTxWhenInDebt(t *testing.T) {
	msgExec := authz.NewMsgExec(testAccAddress(3), []sdk.Msg{&channeltypes.MsgRecvPacket{}})
	nextCalled, err := runDecorator(t,
		mockConsumerKeeper{providerClientFound: true, inDebt: true},
		[]sdk.Msg{&msgExec},
	)
	require.Error(t, err)
	require.True(t, errorsmod.IsOf(err, consumertypes.ErrConsumerInDebt))
	require.False(t, nextCalled)
}

// Safe mode (stale VSC): non-IBC, non-gov msgs are rejected with ErrConsumerInDebt.
func TestSafeModeRejectsAppMsgWhenStale(t *testing.T) {
	// provider client established, not in debt, but VSC stale
	nextCalled, err := runDecorator(t,
		mockConsumerKeeper{providerClientFound: true, inDebt: false, vscStale: true},
		[]sdk.Msg{bankSendMsg()},
	)
	require.Error(t, err)
	require.True(t, errorsmod.IsOf(err, consumertypes.ErrConsumerInDebt))
	require.False(t, nextCalled)

	// /ibc.core.* message must pass (recovery path stays open)
	nextCalled, err = runDecorator(t,
		mockConsumerKeeper{providerClientFound: true, inDebt: false, vscStale: true},
		[]sdk.Msg{&channeltypes.MsgRecvPacket{}},
	)
	require.NoError(t, err)
	require.True(t, nextCalled)
}

func testAccAddress(seed byte) sdk.AccAddress {
	return sdk.AccAddress(bytes.Repeat([]byte{seed}, 20))
}

// TestSafeModeAllowsGovWhenStale verifies that when the consumer has an
// established provider client, is not in debt, but has a stale validator set,
// governance messages still pass (restricted mode allows /cosmos.gov.*).
func TestSafeModeAllowsGovWhenStale(t *testing.T) {
	msg := &govtypes.MsgVote{
		ProposalId: 1,
		Voter:      testAccAddress(1).String(),
		Option:     govtypes.OptionYes,
	}
	nextCalled, err := runDecorator(t,
		mockConsumerKeeper{providerClientFound: true, inDebt: false, vscStale: true},
		[]sdk.Msg{msg},
	)
	require.NoError(t, err)
	require.True(t, nextCalled)
}

// TestRestrictedWhenBothDebtAndStale verifies that when the consumer is both
// in debt and has a stale validator set, app messages (e.g. bank) are rejected.
// The combined condition still routes to restricted mode.
func TestRestrictedWhenBothDebtAndStale(t *testing.T) {
	nextCalled, err := runDecorator(t,
		mockConsumerKeeper{providerClientFound: true, inDebt: true, vscStale: true},
		[]sdk.Msg{bankSendMsg()},
	)
	require.Error(t, err)
	require.True(t, errorsmod.IsOf(err, consumertypes.ErrConsumerInDebt))
	require.False(t, nextCalled)
}

// Pre-pin, the message that establishes the pin must itself pass the filter:
// a new chain starts with no provider client (genesis creates none) and stays
// that way until the owner submits MsgSetProviderClient -- a filter admitting
// only /ibc.* would deadlock the bootstrap, unpinned because unpinned.
func TestMsgFilterDecoratorPrePinAllowsSetProviderClient(t *testing.T) {
	k := mockConsumerKeeper{providerClientFound: false}
	nextCalled, err := runDecorator(t, k, []sdk.Msg{&consumertypes.MsgSetProviderClient{
		Signer:   testAccAddress(1).String(),
		ClientId: "07-tendermint-0",
	}})
	require.NoError(t, err)
	require.True(t, nextCalled)
}

// Pre-pin, governance must also work: the pin is otherwise gated on one owner
// account, and a lost or hostile owner would leave governance unable to act.
func TestMsgFilterDecoratorPrePinAllowsGov(t *testing.T) {
	k := mockConsumerKeeper{providerClientFound: false}
	nextCalled, err := runDecorator(t, k, []sdk.Msg{&govtypes.MsgVote{}})
	require.NoError(t, err)
	require.True(t, nextCalled)
}

// The owner's exec is judged by what it carries: pre-pin, a client recovery
// passes like any IBC message and a parameter update is refused like any
// other message; in restricted mode, the raw send it carries is refused like
// a bare one and the recovery passes.
func TestMsgFilterDecoratorJudgesOwnerExecByItsMessages(t *testing.T) {
	recovery := ownerExecCarrying(t, &clienttypes.MsgRecoverClient{})
	params := ownerExecCarrying(t, &consumertypes.MsgUpdateParams{})
	rawSend := ownerExecCarrying(t, &channeltypesv2.MsgSendPacket{})
	nested := ownerExecCarrying(t, recovery)

	prePin := mockConsumerKeeper{providerClientFound: false}
	nextCalled, err := runDecorator(t, prePin, []sdk.Msg{recovery})
	require.NoError(t, err)
	require.True(t, nextCalled)
	nextCalled, err = runDecorator(t, prePin, []sdk.Msg{params})
	require.Error(t, err)
	require.False(t, nextCalled)
	nextCalled, err = runDecorator(t, prePin, []sdk.Msg{nested})
	require.Error(t, err, "a nested owner exec is not admitted")
	require.False(t, nextCalled)

	inDebt := mockConsumerKeeper{providerClientFound: true, inDebt: true}
	nextCalled, err = runDecorator(t, inDebt, []sdk.Msg{recovery})
	require.NoError(t, err)
	require.True(t, nextCalled)
	nextCalled, err = runDecorator(t, inDebt, []sdk.Msg{rawSend})
	require.Error(t, err)
	require.True(t, errorsmod.IsOf(err, consumertypes.ErrConsumerInDebt))
	require.False(t, nextCalled)
	nextCalled, err = runDecorator(t, inDebt, []sdk.Msg{params})
	require.Error(t, err)
	require.False(t, nextCalled)
}

// The disabled modules stay disabled inside an owner exec.
func TestDisabledModulesDecoratorLooksIntoOwnerExec(t *testing.T) {
	decorator := NewDisabledModulesDecorator("/cosmos.slashing")
	nextCalled := false
	next := func(ctx sdk.Context, _ sdk.Tx, _ bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	}

	_, err := decorator.AnteHandle(sdk.Context{}, mockTx{msgs: []sdk.Msg{ownerExecCarrying(t, &slashingtypes.MsgUpdateParams{})}}, false, next)
	require.Error(t, err)
	require.False(t, nextCalled)

	_, err = decorator.AnteHandle(sdk.Context{}, mockTx{msgs: []sdk.Msg{ownerExecCarrying(t, &clienttypes.MsgRecoverClient{})}}, false, next)
	require.NoError(t, err)
	require.True(t, nextCalled)
}

// Pre-pin, everything else stays rejected: the chain has no synced validator
// set yet, so value-bearing transactions must not execute.
func TestMsgFilterDecoratorPrePinStillRejectsOtherMsgs(t *testing.T) {
	k := mockConsumerKeeper{providerClientFound: false}
	nextCalled, err := runDecorator(t, k, []sdk.Msg{bankSendMsg()})
	require.Error(t, err)
	require.False(t, nextCalled)
}

// Provider client established, in debt: a raw ICS-20 v2 send (the one
// /ibc.core.* message a user originates) is rejected like MsgTransfer is, so
// value stays in while the chain recovers; a relayer delivery still passes.
func TestMsgFilterDecoratorBlocksRawSendPacketWhenInDebt(t *testing.T) {
	_, err := runDecorator(t,
		mockConsumerKeeper{providerClientFound: true, inDebt: true},
		[]sdk.Msg{&channeltypesv2.MsgSendPacket{}},
	)
	require.ErrorIs(t, err, consumertypes.ErrConsumerInDebt)

	nextCalled, err := runDecorator(t,
		mockConsumerKeeper{providerClientFound: true, inDebt: true},
		[]sdk.Msg{&channeltypesv2.MsgRecvPacket{}},
	)
	require.NoError(t, err)
	require.True(t, nextCalled)
}

// Provider client established, in debt: a bank send made only of the photon
// voucher passes (the relayer's refill path), one in any other denom or a
// mixed one does not.
func TestMsgFilterDecoratorAllowsVoucherSendsWhenInDebt(t *testing.T) {
	voucher := ExpectedPhotonDenom("07-tendermint-0")
	keeper := mockConsumerKeeper{providerClientFound: true, inDebt: true}

	nextCalled, err := runDecorator(t, keeper, []sdk.Msg{&banktypes.MsgSend{Amount: sdk.NewCoins(sdk.NewInt64Coin(voucher, 100))}})
	require.NoError(t, err)
	require.True(t, nextCalled)

	_, err = runDecorator(t, keeper, []sdk.Msg{&banktypes.MsgSend{Amount: sdk.NewCoins(sdk.NewInt64Coin("uatone", 100))}})
	require.ErrorIs(t, err, consumertypes.ErrConsumerInDebt)

	_, err = runDecorator(t, keeper, []sdk.Msg{&banktypes.MsgSend{Amount: sdk.NewCoins(sdk.NewInt64Coin(voucher, 100), sdk.NewInt64Coin("uatone", 1))}})
	require.ErrorIs(t, err, consumertypes.ErrConsumerInDebt)
}
