package main

import (
	"fmt"
	"os"

	svrcmd "github.com/cosmos/cosmos-sdk/server/cmd"

	"github.com/allinbits/vaas/app/cmd/provider/cmd"
	app "github.com/allinbits/vaas/app/provider"
	appparams "github.com/allinbits/vaas/app/provider/params"
)

// bech32Prefix is the account address prefix the binary configures. The
// reference apps default to "cosmos"; a deployment that wants its own prefix
// sets VAAS_BECH32_PREFIX in the binary's environment, and the validator and
// consensus prefixes derive from it as the SDK conventions prescribe.
func bech32Prefix() string {
	if prefix := os.Getenv("VAAS_BECH32_PREFIX"); prefix != "" {
		return prefix
	}
	return "cosmos"
}

func main() {
	appparams.SetAddressPrefixes(bech32Prefix())
	rootCmd := cmd.NewRootCmd()
	if err := svrcmd.Execute(rootCmd, "", app.DefaultNodeHome); err != nil {
		fmt.Fprintln(rootCmd.OutOrStderr(), err)
		os.Exit(1)
	}
}
