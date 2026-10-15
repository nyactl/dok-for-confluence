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

	get := a.pageCmd("get [page]", "Show a page as Markdown (page ID or URL)", func(cmd *cobra.Command, svc *core.Service, page string) error {
		p, err := svc.Page(cmd.Context(), page)
		if err != nil {
			return err
		}
		return a.print(cmd, p, func() string { return render.Page(p) })
	})

	children := a.pageCmd("children [page]", "List the pages directly below a page", func(cmd *cobra.Command, svc *core.Service, page string) error {
		max, _ := cmd.Flags().GetInt("max")
		cs, err := svc.Children(cmd.Context(), page, max)
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

	comments := a.pageCmd("comments [page]", "List a page's comments", func(cmd *cobra.Command, svc *core.Service, page string) error {
		max, _ := cmd.Flags().GetInt("max")
		cs, err := svc.Comments(cmd.Context(), page, max)
		if err != nil {
			return err
		}
		return a.print(cmd, cs, func() string { return render.Comments(cs) })
	})
	comments.Flags().Int("max", 50, "maximum number of comments")

	attachments := a.pageCmd("attachments [page]", "List a page's attachments", func(cmd *cobra.Command, svc *core.Service, page string) error {
		as, err := svc.Attachments(cmd.Context(), page, 100)
		if err != nil {
			return err
		}
		return a.print(cmd, as, func() string { return render.Attachments(as) })
	})

	root.AddCommand(whoami, spaces, get, children, search, comments, attachments)
}

// pageCmd is a command on one page; without a page, the user chooses one.
func (a *App) pageCmd(use, short string, run func(*cobra.Command, *core.Service, string) error) *cobra.Command {
	return &cobra.Command{
		Use:               use,
		Short:             short,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: a.completePages,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, _, err := a.service(cmd)
			if err != nil {
				return err
			}
			page, err := a.page(cmd.Context(), svc, args, 0)
			if err != nil {
				return err
			}
			return run(cmd, svc, page)
		},
	}
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
