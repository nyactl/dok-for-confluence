package cli

import (
	"github.com/spf13/cobra"

	"github.com/nyactl/dok-for-confluence/internal/core"
	"github.com/nyactl/dok-for-confluence/internal/render"
)

func (a *App) addReadCommands(root *cobra.Command) {
	whoami := svcCmd(a, "whoami", "Show your account, site, privacy mode and allowed spaces", cobra.NoArgs, func(cmd *cobra.Command, svc *core.Service, _ []string) error {
		me, err := svc.Whoami(cmd.Context())
		if err != nil {
			return err
		}
		return a.print(cmd, me, func() string { return render.Me(me) })
	})

	spaces := svcCmd(a, "spaces", "List the spaces dok may use", cobra.NoArgs, func(cmd *cobra.Command, svc *core.Service, _ []string) error {
		ss := svc.Spaces()
		return a.print(cmd, ss, func() string { return render.Spaces(ss) })
	})

	get := svcCmd(a, "get <page>", "Show a page as Markdown (page ID or URL)", cobra.ExactArgs(1), func(cmd *cobra.Command, svc *core.Service, args []string) error {
		p, err := svc.Page(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return a.print(cmd, p, func() string { return render.Page(p) })
	})

	children := svcCmd(a, "children <page>", "List the pages directly below a page", cobra.ExactArgs(1), func(cmd *cobra.Command, svc *core.Service, args []string) error {
		max, _ := cmd.Flags().GetInt("max")
		cs, err := svc.Children(cmd.Context(), args[0], max)
		if err != nil {
			return err
		}
		return a.print(cmd, cs, func() string { return render.Children(cs) })
	})
	children.Flags().Int("max", 50, "maximum number of pages")

	search := svcCmd(a, "search <text>", "Find pages in the allowed spaces, most recently changed first", cobra.ExactArgs(1), func(cmd *cobra.Command, svc *core.Service, args []string) error {
		max, _ := cmd.Flags().GetInt("max")
		hs, err := svc.Search(cmd.Context(), args[0], max)
		if err != nil {
			return err
		}
		return a.print(cmd, hs, func() string { return render.Search(hs) })
	})
	search.Flags().Int("max", 25, "maximum number of pages")

	comments := svcCmd(a, "comments <page>", "List a page's comments", cobra.ExactArgs(1), func(cmd *cobra.Command, svc *core.Service, args []string) error {
		max, _ := cmd.Flags().GetInt("max")
		cs, err := svc.Comments(cmd.Context(), args[0], max)
		if err != nil {
			return err
		}
		return a.print(cmd, cs, func() string { return render.Comments(cs) })
	})
	comments.Flags().Int("max", 50, "maximum number of comments")

	attachments := svcCmd(a, "attachments <page>", "List a page's attachments", cobra.ExactArgs(1), func(cmd *cobra.Command, svc *core.Service, args []string) error {
		as, err := svc.Attachments(cmd.Context(), args[0], 100)
		if err != nil {
			return err
		}
		return a.print(cmd, as, func() string { return render.Attachments(as) })
	})

	root.AddCommand(whoami, spaces, get, children, search, comments, attachments)
}

// svcCmd builds a command that needs a connected service.
func svcCmd(a *App, use, short string, args cobra.PositionalArgs, run func(*cobra.Command, *core.Service, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  args,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, _, err := a.service(cmd)
			if err != nil {
				return err
			}
			return run(cmd, svc, args)
		},
	}
}
