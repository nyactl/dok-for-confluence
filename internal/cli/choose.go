package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nyactl/approvekit/picker"

	"github.com/nyactl/dok-for-confluence/internal/core"
)

// Chooser lets the user choose a line by typing part of it. Terminals
// implement it; tests script it.
type Chooser interface {
	Choose(opts picker.Options) (picker.Result, error)
}

func (devTTY) Choose(opts picker.Options) (picker.Result, error) {
	r, err := picker.Run(opts)
	if errors.Is(err, picker.ErrNoTerminal) {
		err = fmt.Errorf("%w (%w)", errNoTerminal, err)
	}
	return r, err
}

// errNothingChosen ends a command whose page the user did not choose.
var errNothingChosen = errors.New("nothing chosen; nothing was changed")

// recentPages is how many pages the picker and completion offer.
const recentPages = 200

// page returns the page in args at i, or lets the user choose one of the
// pages in the allowed spaces, most recently changed first.
func (a *App) page(ctx context.Context, svc *core.Service, args []string, i int) (string, error) {
	if len(args) > i {
		return args[i], nil
	}
	ch, ok := a.TTY.(Chooser)
	if !ok {
		return "", errors.New("give a page ID or URL, or run this in a terminal to choose one")
	}
	hits, err := svc.RecentPages(ctx, recentPages)
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return "", errors.New("the allowed spaces hold no pages yet; give a page ID or URL")
	}
	lines := make([]string, len(hits))
	for k, h := range hits {
		lines[k] = fmt.Sprintf("%s  [%s]  %s  (%s)", h.Title, h.Space, h.ID, h.Updated)
	}
	r, err := ch.Choose(picker.Options{Title: "Choose a page", Items: lines})
	switch {
	case errors.Is(err, picker.ErrCanceled):
		return "", errNothingChosen
	case errors.Is(err, errNoTerminal):
		return "", errors.New("give a page ID or URL, or run this in a terminal to choose one")
	case err != nil:
		return "", err
	}
	return hits[r.Index].ID, nil
}

// completePages completes page IDs with their titles, from the pages of
// the allowed spaces.
func (a *App) completePages(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	svc, _, err := a.service(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveError
	}
	hits, err := svc.RecentPages(cmd.Context(), recentPages)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveError
	}
	var out []cobra.Completion
	for _, h := range hits {
		if strings.HasPrefix(h.ID, toComplete) {
			out = append(out, cobra.CompletionWithDesc(h.ID, oneLine(h.Title+" ["+h.Space+"]")))
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// completeMacros completes the names of allowed macros.
func (a *App) completeMacros(cmd *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	_, cfg, err := a.loadConfig(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveError
	}
	var out []cobra.Completion
	for _, d := range cfg.Macros.Allowed() {
		if strings.HasPrefix(d.Name, toComplete) {
			out = append(out, cobra.CompletionWithDesc(d.Name, oneLine(d.Title)))
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// oneLine keeps a completion description on one line and free of control
// characters, which shells would show raw.
func oneLine(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r < ' ' || r == 0x7f }), " ")
}
