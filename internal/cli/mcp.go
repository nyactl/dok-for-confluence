package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nyactl/dok-for-confluence/internal/mcpserver"
)

func (a *App) mcpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run the MCP server over stdio",
		Long: "Runs dok as an MCP server for MCP clients.\n" +
			"Read-only unless --allow-writes is given. Every write is shown to you in an approval dialog first;\n" +
			"clients that cannot show such dialogs cannot write.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, cfg, err := a.service(cmd)
			if err != nil {
				return err
			}
			prof, _ := a.profile(cmd)
			writes, _ := cmd.Flags().GetBool("allow-writes")
			_, _ = fmt.Fprintf(a.Err, "dok %s: serving %s (profile %s, spaces %s, privacy %s, writes %v)\n", a.Version, cfg.Site, prof, strings.Join(cfg.Spaces, ","), cfg.Privacy, writes)
			return mcpserver.Run(cmd.Context(), svc, mcpserver.Options{Version: a.Version, AllowWrites: writes, Target: target(cfg.Site, prof)})
		},
	}
	cmd.Flags().Bool("allow-writes", false, "offer write tools (each write still needs your approval)")
	return cmd
}
