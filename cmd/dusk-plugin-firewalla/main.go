// Command dusk-plugin-firewalla keeps a Firewalla box's network inventory in
// Dusk: its networks, every device it has seen, and the box itself.
//
// The host execs it and hands it a unix socket to serve PluginService on. It is
// not useful on its own, and says so rather than starting and waiting.
package main

import (
	"flag"
	"log/slog"
	"os"

	sdk "github.com/NerdsWhoFish/dusk-plugin-sdk/plugin"

	"github.com/NerdsWhoFish/dusk-plugin-firewalla/internal/serve"
)

// version is set at build time.
var version = "dev"

func main() {
	// A flag covers running this by hand against grpcurl, which is the only way
	// to debug it in isolation. The host sets the environment instead.
	socket := flag.String("socket", "", "unix socket to serve on")
	flag.Parse()

	err := sdk.Run(&serve.Server{Version: version}, sdk.Options{
		Socket:  *socket,
		Version: version,
	})
	if err != nil {
		slog.Error("dusk-plugin-firewalla", "error", err)
		os.Exit(1)
	}
}
