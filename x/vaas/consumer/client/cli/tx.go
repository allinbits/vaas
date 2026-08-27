package cli

import (
	"fmt"

	"github.com/allinbits/vaas/x/vaas/consumer/types"
	"github.com/spf13/cobra"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/client/tx"
)

// NewTxCmd returns the root tx command for the consumer module.
func NewTxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                        types.ModuleName,
		Short:                      "VAAS consumer transaction subcommands",
		DisableFlagParsing:         true,
		SuggestionsMinimumDistance: 2,
		RunE:                       client.ValidateCmd,
	}
	cmd.AddCommand(NewSetProviderClientCmd())
	return cmd
}

// NewSetProviderClientCmd pins the provider IBC client, once, at bootstrap.
func NewSetProviderClientCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set-provider-client [client-id]",
		Short: "Pin the IBC client the consumer treats as the provider (owner or governance, exactly once)",
		Long: `Pin the IBC client the consumer treats as the provider.

A new consumer starts with no provider client and rejects every validator-set
packet until this pin is set. Only the owner the provider seeded into the
consumer params (or the governance authority) may set it, exactly once: the
named client must be an Active tendermint client tracking the provider chain
id pinned at genesis, with a registered IBC v2 counterparty. Recovering an
expired or frozen pinned client afterwards is governance's MsgRecoverClient,
which substitutes fresh client state under the same client id.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}
			msg := &types.MsgSetProviderClient{
				Signer:   clientCtx.GetFromAddress().String(),
				ClientId: args[0],
			}
			if err := msg.ValidateBasic(); err != nil {
				return fmt.Errorf("invalid set-provider-client message: %w", err)
			}
			return tx.GenerateOrBroadcastTxCLI(clientCtx, cmd.Flags(), msg)
		},
	}
	flags.AddTxFlagsToCmd(cmd)
	return cmd
}
