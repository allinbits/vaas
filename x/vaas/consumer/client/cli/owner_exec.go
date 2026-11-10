package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/allinbits/vaas/x/vaas/consumer/types"
)

// NewOwnerExecCmd executes messages as the consumer module account, on the
// owner's say-so.
func NewOwnerExecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "owner-exec [messages-file]",
		Short: "Execute messages as the consumer module account (owner only)",
		Long: `Execute messages as the consumer module account, the authority the
reference consumer app names for IBC, upgrades, consensus and every module's
parameters. Only the owner the provider seeded into the consumer params may
submit it; every message in the file must name that account as its signer
("query vaasconsumer owner-authority" prints it), and the messages execute in
order, all or nothing.

The file holds the messages proto-JSON encoded, like a governance proposal's:

{
  "messages": [
    {
      "@type": "/ibc.core.client.v1.MsgRecoverClient",
      "subject_client_id": "07-tendermint-0",
      "substitute_client_id": "07-tendermint-1",
      "signer": "<owner-authority>"
    }
  ]
}`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}
			msgs, err := parseOwnerExecMessages(clientCtx.Codec, args[0])
			if err != nil {
				return err
			}
			msg, err := types.NewMsgOwnerExec(clientCtx.GetFromAddress().String(), msgs)
			if err != nil {
				return err
			}
			if err := msg.ValidateBasic(); err != nil {
				return fmt.Errorf("invalid owner-exec message: %w", err)
			}
			return tx.GenerateOrBroadcastTxCLI(clientCtx, cmd.Flags(), msg)
		},
	}
	flags.AddTxFlagsToCmd(cmd)
	return cmd
}

// parseOwnerExecMessages reads the messages file: a JSON object whose
// "messages" array holds proto-JSON encoded messages, each with its "@type".
func parseOwnerExecMessages(cdc codec.Codec, path string) ([]sdk.Msg, error) {
	contents, err := os.ReadFile(path) //nolint:gosec // the operator names the file
	if err != nil {
		return nil, err
	}
	var file struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(contents, &file); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	msgs := make([]sdk.Msg, len(file.Messages))
	for i, raw := range file.Messages {
		var msg sdk.Msg
		if err := cdc.UnmarshalInterfaceJSON(raw, &msg); err != nil {
			return nil, fmt.Errorf("parsing message %d of %s: %w", i, path, err)
		}
		msgs[i] = msg
	}
	return msgs, nil
}
