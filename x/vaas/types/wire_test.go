package types_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtprotocrypto "github.com/cometbft/cometbft/proto/tendermint/crypto"

	"cosmossdk.io/math"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/allinbits/vaas/x/vaas/types"
)

func TestValidatorSetChangePacketData_IsSnapshotRoundTrip(t *testing.T) {
	pkt := types.NewValidatorSetChangePacketData(nil, 7)
	pkt.IsSnapshot = true

	bz := pkt.GetBytes()
	var got types.ValidatorSetChangePacketData
	require.NoError(t, types.ModuleCdc.UnmarshalJSON(bz, &got))
	require.True(t, got.IsSnapshot)
	require.Equal(t, uint64(7), got.ValsetUpdateId)
}

// TestValidatorSetChangePacketDataValidateValidatorUpdates pins that Validate
// rejects a validator update whose consensus pubkey cannot be decoded or whose
// power is negative, so a malformed VSC packet is rejected on receipt (an
// error acknowledgement) instead of being accepted and later panicking the
// consumer at EndBlock when ApplyCCValidatorChanges decodes the pubkey.
func TestValidatorSetChangePacketDataValidateValidatorUpdates(t *testing.T) {
	goodPK, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	t.Run("valid updates pass", func(t *testing.T) {
		pkt := types.NewValidatorSetChangePacketData(
			[]abci.ValidatorUpdate{{PubKey: goodPK, Power: 100}, {PubKey: goodPK, Power: 0}},
			1,
		)
		require.NoError(t, pkt.Validate())
	})

	t.Run("empty update set passes", func(t *testing.T) {
		require.NoError(t, types.NewValidatorSetChangePacketData(nil, 1).Validate())
	})

	t.Run("undecodable pubkey is rejected", func(t *testing.T) {
		// An empty PublicKey oneof (no key set) does not decode to any
		// supported consensus key type.
		pkt := types.NewValidatorSetChangePacketData(
			[]abci.ValidatorUpdate{{PubKey: cmtprotocrypto.PublicKey{}, Power: 5}},
			1,
		)
		err := pkt.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "undecodable consensus pubkey")
	})

	t.Run("negative power is rejected", func(t *testing.T) {
		pkt := types.NewValidatorSetChangePacketData(
			[]abci.ValidatorUpdate{{PubKey: goodPK, Power: -1}},
			1,
		)
		err := pkt.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "negative power")
	})
}

func TestEvidencePacketDataBitmapValidation(t *testing.T) {
	addr := sdk.ConsAddress([]byte("consaddr20bytes....."))
	minSigned := math.LegacyMustNewDecFromStr("0.5")
	// span 8, heights 100..107, missed at 100,101,102,103,104 (5 of 8)
	bitmap := []byte{0b00011111}
	p := types.NewEvidencePacketData(addr, 100, bitmap, 8, 600, minSigned)
	require.NoError(t, p.Validate())
	require.Equal(t, int64(107), p.WindowEndHeight)
	require.Equal(t, int64(8), p.Span())
	require.Equal(t, int64(5), p.MissedCount())

	// span exceeding window is invalid
	p2 := types.NewEvidencePacketData(addr, 100, bitmap, 8, 4, minSigned)
	require.Error(t, p2.Validate())

	// bitmap shorter than span is invalid
	p3 := types.NewEvidencePacketData(addr, 100, []byte{}, 8, 600, minSigned)
	require.Error(t, p3.Validate())

	// JSON round-trip preserves everything
	var back types.EvidencePacketData
	require.NoError(t, json.Unmarshal(p.GetBytes(), &back))
	require.Equal(t, p, back)
}

func TestEvidencePacketDataMaxMissed(t *testing.T) {
	addr := sdk.ConsAddress([]byte("consaddr20bytes....."))

	// maxMissed = window - ceil(minSigned*window) = 8 - ceil(0.5*8) = 8 - 4 = 4
	p := types.NewEvidencePacketData(addr, 100, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	require.Equal(t, int64(4), p.MaxMissed())

	// maxMissed = 600 - ceil(0.5*600) = 600 - 300 = 300
	p2 := types.NewEvidencePacketData(addr, 100, []byte{0xFF}, 8, 600, math.LegacyMustNewDecFromStr("0.5"))
	require.Equal(t, int64(300), p2.MaxMissed())
}

// TestEvidencePacketDataValidateNilMinSignedPerWindow pins that a JSON
// payload omitting min_signed_per_window fails Validate with an error rather
// than nil-panicking on the nil-backed dec.
func TestEvidencePacketDataValidateNilMinSignedPerWindow(t *testing.T) {
	packet := types.NewEvidencePacketData(
		sdk.ConsAddress([]byte{0x01, 0x02, 0x03, 0x04, 0x05}), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"),
	)

	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(packet.GetBytes(), &fields))
	delete(fields, "min_signed_per_window")
	bz, err := json.Marshal(fields)
	require.NoError(t, err)

	var decoded types.EvidencePacketData
	require.NoError(t, json.Unmarshal(bz, &decoded))
	require.True(t, decoded.MinSignedPerWindow.IsNil())

	require.NotPanics(t, func() {
		err := decoded.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "min signed per window cannot be nil")
	})
}
