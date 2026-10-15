package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nyactl/dok-for-confluence/internal/config"
	"github.com/nyactl/dok-for-confluence/internal/macros"
)

func (a *App) macrosCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "macros",
		Short: "Choose the macros dok may create",
		Long: "Pages show allowed macros as ```macro:NAME blocks, which dok checks and turns into the macro. Other macros stay as they are.\n" +
			"Built-in: " + strings.Join(builtinNames(), ", ") + ". Others are learned from a page where the macro was inserted once in the editor.\n" +
			"Macros that show content from elsewhere or embed code are never created.",
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List the allowed macros",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, cfg, err := a.loadConfig(cmd)
			if err != nil {
				return err
			}
			return a.print(cmd, cfg.Macros, func() string {
				var b strings.Builder
				for _, d := range cfg.Macros.Allowed() {
					kind := "learned"
					if d.Builtin {
						kind = "built-in"
					}
					fmt.Fprintf(&b, "- %s (%s, %s): %s\n", d.Name, d.Title, kind, d.ParamNames())
				}
				for _, lang := range slices.Sorted(mapKeys(cfg.Macros.Fences)) {
					f := cfg.Macros.Fences[lang]
					fmt.Fprintf(&b, "- fenced %s code becomes %s, in %s\n", lang, f.Macro, f.Param)
				}
				if b.Len() == 0 {
					return "No macros are allowed yet; see `dok config macros --help`.\n"
				}
				return b.String()
			})
		},
	}
	allow := &cobra.Command{
		Use:   "allow NAME...",
		Short: "Allow built-in macros: " + strings.Join(builtinNames(), ", "),
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.saveConfig(cmd, func(cfg *config.Config) (string, error) {
				builtin := macros.Builtin()
				for _, name := range args {
					if _, ok := builtin[name]; !ok {
						return "", fmt.Errorf("%s is not a built-in macro (%s); learn other macros from a page", name, strings.Join(builtinNames(), ", "))
					}
					if !slices.Contains(cfg.Macros.Builtin, name) {
						cfg.Macros.Builtin = append(cfg.Macros.Builtin, name)
					}
				}
				return "Allowed macros: " + allowedNames(cfg) + ".", nil
			})
		},
	}
	remove := &cobra.Command{
		Use:               "remove NAME...",
		Short:             "Stop allowing macros",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: a.completeMacros,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.saveConfig(cmd, func(cfg *config.Config) (string, error) {
				for _, name := range args {
					before := len(cfg.Macros.Builtin) + len(cfg.Macros.Learned)
					cfg.Macros.Builtin = slices.DeleteFunc(cfg.Macros.Builtin, func(n string) bool { return n == name })
					cfg.Macros.Learned = slices.DeleteFunc(cfg.Macros.Learned, func(d macros.Def) bool { return d.Name == name })
					if len(cfg.Macros.Builtin)+len(cfg.Macros.Learned) == before {
						return "", fmt.Errorf("macro %s is not allowed", name)
					}
					for lang, f := range cfg.Macros.Fences {
						if f.Macro == name {
							delete(cfg.Macros.Fences, lang)
						}
					}
				}
				return "Allowed macros: " + allowedNames(cfg) + ".", nil
			})
		},
	}
	learn := &cobra.Command{
		Use:   "learn <page>",
		Short: "Learn the macros on a page, each confirmed in the terminal",
		Long: "Reads the macros on a page in an allowed space and proposes each kind as a macro dok may create. A definition holds the macro's identity and its settings,\n" +
			"never the page's text or people: text values are left out until a macro block sets them. Each one is shown and must be confirmed.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, cfg, err := a.service(cmd)
			if err != nil {
				return err
			}
			taken := map[string]bool{}
			for _, d := range cfg.Macros.Allowed() {
				taken[d.Name] = true
			}
			defs, skipped, err := svc.LearnMacros(cmd.Context(), args[0], taken)
			if err != nil {
				return err
			}
			for _, s := range skipped {
				_, _ = fmt.Fprintln(a.Err, "skipped "+s)
			}
			if len(defs) == 0 {
				return errors.New("the page holds no macros that can be learned")
			}
			var learned []macros.Def
			for _, d := range defs {
				desc := fmt.Sprintf("Allow macro %s: %q (%s, %s)\nParameters: %s", d.Name, d.Title, d.ExtensionType, d.ExtensionKey, describeParams(d))
				if err := confirm(a.TTY, desc, "", false); errors.Is(err, errNoTerminal) {
					return err
				} else if err != nil {
					_, _ = fmt.Fprintln(a.Err, "not allowed: "+d.Name)
					continue
				}
				learned = append(learned, d)
			}
			if len(learned) == 0 {
				return errors.New("no macro was allowed")
			}
			return a.saveConfig(cmd, func(cfg *config.Config) (string, error) {
				cfg.Macros.Learned = append(cfg.Macros.Learned, learned...)
				return "Allowed macros: " + allowedNames(cfg) + ".", nil
			})
		},
	}
	fence := &cobra.Command{
		Use:   "fence <language> <macro> <parameter>",
		Short: "Turn fenced code of a language, such as mermaid, into an allowed macro",
		Long:  "Fenced code in that language becomes the macro, with the code in the named text parameter, and the macro reads back as the same fenced code.",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.saveConfig(cmd, func(cfg *config.Config) (string, error) {
				if cfg.Macros.Fences == nil {
					cfg.Macros.Fences = map[string]macros.Fence{}
				}
				cfg.Macros.Fences[args[0]] = macros.Fence{Macro: args[1], Param: args[2]}
				return fmt.Sprintf("Fenced %s code becomes macro %s.", args[0], args[1]), nil
			})
		},
	}
	unfence := &cobra.Command{
		Use:   "unfence <language>",
		Short: "Keep fenced code of a language as code again",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.saveConfig(cmd, func(cfg *config.Config) (string, error) {
				if _, ok := cfg.Macros.Fences[args[0]]; !ok {
					return "", fmt.Errorf("fenced %s code is not turned into a macro", args[0])
				}
				delete(cfg.Macros.Fences, args[0])
				return fmt.Sprintf("Fenced %s code stays code.", args[0]), nil
			})
		},
	}
	cmd.AddCommand(list, allow, remove, learn, fence, unfence)
	return cmd
}

func builtinNames() []string { return slices.Sorted(mapKeys(macros.Builtin())) }

func allowedNames(cfg *config.Config) string {
	var names []string
	for _, d := range cfg.Macros.Allowed() {
		names = append(names, d.Name)
	}
	return orDash(strings.Join(names, ", "))
}

func describeParams(d macros.Def) string {
	if len(d.Params) == 0 {
		return "none"
	}
	parts := make([]string, len(d.Params))
	for i, p := range d.Params {
		parts[i] = p.Name + " (" + p.Kind + ")"
	}
	return strings.Join(parts, ", ")
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}
