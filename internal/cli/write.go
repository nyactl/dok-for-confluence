package cli

import (
	"github.com/spf13/cobra"

	"github.com/nyactl/dok-for-confluence/internal/core"
)

func (a *App) addWriteCommands(root *cobra.Command) {
	create := svcCmd(a, "create", "Create a page from Markdown, as a draft unless --publish is given", cobra.NoArgs, func(cmd *cobra.Command, svc *core.Service, _ []string) error {
		body, err := a.fileArg(cmd)
		if err != nil {
			return err
		}
		space, _ := cmd.Flags().GetString("space")
		parent, _ := cmd.Flags().GetString("parent")
		title, _ := cmd.Flags().GetString("title")
		publish, _ := cmd.Flags().GetBool("publish")
		attach, _ := cmd.Flags().GetStringArray("attach")
		p, err := svc.PlanCreate(cmd.Context(), core.CreateInput{Space: space, ParentID: parent, Title: title, Markdown: body, Publish: publish, Attach: attach})
		return a.apply(cmd, svc, p, err)
	})
	create.Flags().String("space", "", "space key (optional with --parent)")
	create.Flags().String("parent", "", "parent page ID or URL")
	create.Flags().String("title", "", "page title")
	create.Flags().String("file", "", "Markdown file with the content, or - for stdin")
	create.Flags().Bool("publish", false, "publish right away instead of creating a draft")
	create.Flags().StringArray("attach", nil, "a file to attach first, for an image written as ![name](attachment:name); repeat for more")

	update := a.pageCmd("update [page]", "Replace a page's content with Markdown; fails if the page changed since --version", func(cmd *cobra.Command, svc *core.Service, page string) error {
		body, err := a.fileArg(cmd)
		if err != nil {
			return err
		}
		version, _ := cmd.Flags().GetInt("version")
		title, _ := cmd.Flags().GetString("title")
		message, _ := cmd.Flags().GetString("message")
		attach, _ := cmd.Flags().GetStringArray("attach")
		p, err := svc.PlanUpdate(cmd.Context(), core.UpdateInput{Page: page, Markdown: body, Title: title, Version: version, Message: message, Attach: attach})
		return a.apply(cmd, svc, p, err)
	})
	update.Flags().String("file", "", "Markdown file with the new content, or - for stdin")
	update.Flags().Int("version", 0, "the version your change is based on, as shown by `dok get`")
	update.Flags().String("title", "", "new title (default: keep it)")
	update.Flags().String("message", "", "version comment")
	update.Flags().StringArray("attach", nil, "a file to attach first, for an image written as ![name](attachment:name); repeat for more")

	publish := svcCmd(a, "publish <page>", "Publish a draft", cobra.ExactArgs(1), func(cmd *cobra.Command, svc *core.Service, args []string) error {
		p, err := svc.PlanPublish(cmd.Context(), args[0])
		return a.apply(cmd, svc, p, err)
	})

	comment := a.pageCmd("comment [page]", "Add a comment written in Markdown; mention people as @[label], as shown by dok", func(cmd *cobra.Command, svc *core.Service, page string) error {
		body, err := a.textArg(cmd, "body")
		if err != nil {
			return err
		}
		p, err := svc.PlanComment(cmd.Context(), page, body)
		return a.apply(cmd, svc, p, err)
	})
	comment.Flags().String("body", "", "comment text in Markdown, or - for stdin")

	attach := svcCmd(a, "attach <page> <file>", "Upload a local file to a page", cobra.ExactArgs(2), func(cmd *cobra.Command, svc *core.Service, args []string) error {
		p, err := svc.PlanAttach(cmd.Context(), args[0], args[1])
		return a.apply(cmd, svc, p, err)
	})

	attach.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return a.completePages(cmd, args, toComplete)
		}
		return nil, cobra.ShellCompDirectiveDefault // the file
	}
	root.AddCommand(create, update, publish, comment, attach)
}
