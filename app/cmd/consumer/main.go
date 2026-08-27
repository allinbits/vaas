package main

import (
	"fmt"
	"os"

	"github.com/allinbits/vaas/app/cmd/consumer/cmd"
	app "github.com/allinbits/vaas/app/consumer"
	appparams "github.com/allinbits/vaas/app/consumer/params"

	svrcmd "github.com/cosmos/cosmos-sdk/server/cmd"
)

func main() {
	appparams.SetAddressPrefixes("cosmos")
	rootCmd := cmd.NewRootCmd()
	if err := svrcmd.Execute(rootCmd, "", app.DefaultNodeHome); err != nil {
		fmt.Fprintln(rootCmd.OutOrStderr(), err)
		os.Exit(1)
	}
}
