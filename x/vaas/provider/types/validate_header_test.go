package types_test

// validate_header_test.go covers ValidateHeaderForConsumerDoubleVoting, the
// shared guard MsgSubmitConsumerDoubleVoting.ValidateBasic runs over the
// infraction block header before anything dereferences it. Each of its own
// nil checks is exercised, plus the two classes of failure it delegates to
// ibctm Header.ValidateBasic (malformed signed header, validator set that does
// not hash to the header's ValidatorsHash), plus a fully valid header.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	tmtypes "github.com/cometbft/cometbft/types"

	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	"github.com/allinbits/vaas/x/vaas/provider/types"
)

func TestValidateHeaderForConsumerDoubleVoting(t *testing.T) {
	setupBech32Cfg()

	const (
		chainID = "consumer-1"
		height  = int64(10)
	)

	// validHeader returns a fresh, fully valid header on every call so cases
	// can mutate one field without leaking into the next case.
	validHeader := func() *ibctmtypes.Header {
		signer := tmtypes.NewMockPV()
		valSet := tmtypes.NewValidatorSet([]*tmtypes.Validator{
			tmtypes.NewValidator(signer.PrivKey.PubKey(), 1),
		})
		return makeValidIBCTMHeader(t, chainID, height, valSet, time.Now().UTC())
	}

	testCases := []struct {
		name    string
		header  func() *ibctmtypes.Header
		wantErr string
	}{
		{
			name:    "nil header",
			header:  func() *ibctmtypes.Header { return nil },
			wantErr: "infraction block header cannot be nil",
		},
		{
			name:    "nil signed header",
			header:  func() *ibctmtypes.Header { return &ibctmtypes.Header{} },
			wantErr: "signed header or header cannot be nil",
		},
		{
			name: "signed header with nil inner header",
			header: func() *ibctmtypes.Header {
				return &ibctmtypes.Header{SignedHeader: &tmproto.SignedHeader{}}
			},
			wantErr: "signed header or header cannot be nil",
		},
		{
			name: "nil validator set",
			header: func() *ibctmtypes.Header {
				h := validHeader()
				h.ValidatorSet = nil
				return h
			},
			wantErr: "validator set cannot be nil",
		},
		{
			name: "commit height diverging from the header height",
			header: func() *ibctmtypes.Header {
				h := validHeader()
				h.SignedHeader.Commit.Height = height + 1
				return h
			},
			wantErr: "header failed basic validation",
		},
		{
			name: "validator set not matching the header's validators hash",
			header: func() *ibctmtypes.Header {
				h := validHeader()
				// Swap in a foreign validator set rather than editing
				// ValidatorsHash: editing the header would change its hash and
				// trip the signed-header commit check first.
				other := tmtypes.NewMockPV()
				otherSet, err := tmtypes.NewValidatorSet([]*tmtypes.Validator{
					tmtypes.NewValidator(other.PrivKey.PubKey(), 1),
				}).ToProto()
				require.NoError(t, err)
				h.ValidatorSet = otherSet
				return h
			},
			wantErr: "validator set does not match hash",
		},
		{
			name:   "valid header",
			header: validHeader,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			require.NotPanics(t, func() {
				err = types.ValidateHeaderForConsumerDoubleVoting(tc.header())
			})
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
