package main

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/gayanmadurapperuma/codegraph/internal/mcpserver"
)

// version is set at release time with -ldflags "-X main.version=…".
var version = "dev"

func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the MCP server over stdio for this project",
		Long: "Run the MCP server over stdio. The project root is fixed at start (--root or the\n" +
			"current directory). Tools: find_relevant, get_symbol, callers_of, callees_of, impact_of.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openProject()
			if err != nil {
				return err
			}
			return mcpserver.New(p, version).Run(cmd.Context(), &mcp.StdioTransport{})
		},
	}
}
