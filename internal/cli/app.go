// Package cli implements the dok command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nyactl/approvekit/atlassian"
	"github.com/nyactl/approvekit/keychain"
	"github.com/nyactl/approvekit/people"
	"github.com/nyactl/approvekit/profile"
	"github.com/nyactl/approvekit/sanitize"

	"github.com/nyactl/dok-for-confluence/internal/config"
	"github.com/nyactl/dok-for-confluence/internal/confluence"
	"github.com/nyactl/dok-for-confluence/internal/core"
	"github.com/nyactl/dok-for-confluence/internal/render"
)

// App holds the command dependencies, replaceable in tests.
type App struct {
	Version  string
	In       io.Reader
	Out, Err io.Writer
	TTY      Terminal
	Secrets  keychain.Store
	Now      func() time.Time
	// Transport overrides the HTTP transport in tests.
	Transport http.RoundTripper
	// Gateway overrides Atlassian's API gateway in tests.
	Gateway string
}

// Default returns the production dependencies.
func Default(version string) *App {
	return &App{Version: resolveVersion(version), In: os.Stdin, Out: os.Stdout, Err: os.Stderr, TTY: devTTY{}, Secrets: keychain.New("dok-for-confluence"), Now: time.Now}
}

func resolveVersion(v string) string {
	if v != "" && v != "dev" {
		return v
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

// Execute runs the CLI with the given arguments.
func (a *App) Execute(ctx context.Context, args []string) error {
	root := a.root()
	root.SetArgs(args)
	root.SetOut(a.Out)
	root.SetErr(sanitizeWriter{a.Err})
	_, err := root.ExecuteContextC(ctx)
	return err
}

type sanitizeWriter struct{ w io.Writer }

func (s sanitizeWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(s.w, sanitize.String(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (a *App) root() *cobra.Command {
	root := &cobra.Command{
		Use:           "dok",
		Short:         "Read and write Confluence Cloud pages from the terminal or an MCP client",
		Long:          "dok reads and changes pages in the Confluence spaces you allow. Every change asks for confirmation in the terminal.\nRun `dok auth login` first, then `dok config spaces add KEY`.",
		SilenceUsage:  true,
		SilenceErrors: false,
		Version:       a.Version,
	}
	root.PersistentFlags().Bool("json", false, "print JSON instead of Markdown")
	root.PersistentFlags().String("profile", "", "config profile to use, e.g. test (default: $DOK_PROFILE or \"default\")")
	root.AddCommand(a.authCmd(), a.configCmd(), a.mcpCmd())
	a.addReadCommands(root)
	a.addWriteCommands(root)
	return root
}

func (a *App) print(cmd *cobra.Command, v any, markdown func() string) error {
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		s, err := render.JSON(v)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(a.Out, s)
		return err
	}
	_, err := io.WriteString(a.Out, markdown())
	return err
}

var errDevNeedsProfile = errors.New("this is a development build: choose a profile explicitly with --profile or DOK_PROFILE, so it cannot touch your production site by accident")

// checkDevBuild keeps development builds away from production: they may only
// use a non-default profile that explicitly allows them.
func (a *App) checkDevBuild(prof string, cfg *config.Config) error {
	var account *profile.Account
	if cfg != nil {
		account = &cfg.Account
	}
	if err := profile.CheckDevBuild(a.Version, prof, account); err != nil {
		return fmt.Errorf("%w; opt in with `dok config dev-builds on`", err)
	}
	return nil
}

// profile returns the selected profile name. Development builds must name
// one explicitly.
func (a *App) profile(cmd *cobra.Command) (string, error) {
	p, _ := cmd.Flags().GetString("profile")
	if !profile.IsRelease(a.Version) && !config.Store.Explicit(p) {
		return "", errDevNeedsProfile
	}
	return config.ResolveProfile(p)
}

// target describes where a write goes, so the human sees it when confirming.
func target(site, profile string) string {
	return fmt.Sprintf("Confluence site: %s (profile %s)", site, profile)
}

// loadConfig loads the selected profile, applying the development-build lock.
func (a *App) loadConfig(cmd *cobra.Command) (string, *config.Config, error) {
	prof, err := a.profile(cmd)
	if err != nil {
		return "", nil, err
	}
	cfg, err := config.Load(prof)
	if err != nil {
		return "", nil, err
	}
	if err := a.checkDevBuild(prof, cfg); err != nil {
		return "", nil, err
	}
	return prof, cfg, nil
}

// service loads the profile's settings and token and connects to Confluence.
func (a *App) service(cmd *cobra.Command) (*core.Service, *config.Config, error) {
	prof, cfg, err := a.loadConfig(cmd)
	if err != nil {
		return nil, nil, err
	}
	if w := cfg.ExpiryWarning(a.Now()); w != "" {
		_, _ = fmt.Fprintln(a.Err, "warning: "+w)
	}
	token, err := a.Secrets.Get(keychain.AccountKey(cfg.Site, cfg.Email))
	if errors.Is(err, keychain.ErrNotFound) {
		err = fmt.Errorf("%w; run `dok auth login`", err)
	}
	if err != nil {
		return nil, nil, err
	}
	cloudID := ""
	if cfg.ScopedToken {
		cloudID = cfg.CloudID
	}
	svc, err := core.New(cmd.Context(), a.client(cfg.Site, cfg.Email, token, cloudID),
		core.Settings{Privacy: people.Mode(cfg.Privacy), Spaces: cfg.Spaces, Parents: cfg.Parents, Macros: cfg.Macros.Allowed(), Fences: cfg.Macros.Fences})
	if errors.Is(err, atlassian.ErrUnauthorized) {
		if cfg.TokenExpiry != "" {
			err = fmt.Errorf("%w (recorded expiry: %s)", err, cfg.TokenExpiry)
		}
		err = fmt.Errorf("%w; then run `dok auth login`", err)
	}
	if err != nil {
		return nil, nil, err
	}
	if path, err := config.Path(prof); err == nil {
		svc.KeepLabels(people.NewFileStore(strings.TrimSuffix(path, ".json") + ".labels.json"))
	}
	return svc, cfg, nil
}

// client returns a Confluence client; a non-empty cloudID routes it through
// the API gateway, as scoped tokens require.
func (a *App) client(site, email, token, cloudID string) *confluence.Client {
	var opts []atlassian.Option
	if a.Transport != nil {
		opts = append(opts, atlassian.WithTransport(a.Transport))
	}
	if cloudID != "" {
		gw := a.Gateway
		if gw == "" {
			gw = atlassian.Gateway
		}
		opts = append(opts, atlassian.WithGateway(gw, cloudID))
	}
	return confluence.New(site, email, token, a.Version, opts...)
}

// apply asks for confirmation on the terminal and applies the plan.
func (a *App) apply(cmd *cobra.Command, svc *core.Service, plan *core.Plan, err error) error {
	if err != nil {
		return err
	}
	prof, err := a.profile(cmd)
	if err != nil {
		return err
	}
	desc := target(svc.Site(), prof) + "\n\n" + render.Plan(plan)
	if err := confirm(a.TTY, desc, plan.Target, plan.Kind == core.Destructive); err != nil {
		return err
	}
	msg, err := plan.Apply(cmd.Context())
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(a.Out, sanitize.String(msg))
	return err
}

// textArg reads --<name>; "-" reads stdin.
func (a *App) textArg(cmd *cobra.Command, name string) (string, error) {
	v, _ := cmd.Flags().GetString(name)
	if v != "-" {
		return v, nil
	}
	b, err := io.ReadAll(io.LimitReader(a.In, 4<<20))
	return strings.TrimRight(string(b), "\n"), err
}

// fileArg reads the Markdown file named by --file; "-" reads stdin.
func (a *App) fileArg(cmd *cobra.Command) (string, error) {
	p, _ := cmd.Flags().GetString("file")
	switch p {
	case "":
		return "", errors.New("give the content with --file page.md (or --file - for stdin)")
	case "-":
		b, err := io.ReadAll(io.LimitReader(a.In, 4<<20))
		return string(b), err
	}
	b, err := os.ReadFile(p) //nolint:gosec // G304: the user names the file to publish
	return string(b), err
}
