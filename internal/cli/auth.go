package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nyactl/approvekit/atlassian"
	"github.com/nyactl/approvekit/keychain"
	"github.com/nyactl/approvekit/profile"
	"github.com/nyactl/dok-for-confluence/internal/config"
	"github.com/nyactl/dok-for-confluence/internal/confluence"
	"github.com/nyactl/dok-for-confluence/internal/render"
)

func (a *App) authCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "auth", Short: "Log in, check or remove your Confluence credentials"}

	login := &cobra.Command{
		Use:   "login",
		Short: "Store your Atlassian site, email and API token",
		Long: "Asks for your API token in the terminal (input hidden), verifies it and stores it in the macOS Keychain.\n" +
			"Create a token at https://id.atlassian.com/manage-profile/security/api-tokens, preferably a scoped one for Confluence (see the README).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			site, _ := cmd.Flags().GetString("site")
			email, _ := cmd.Flags().GetString("email")
			expires, _ := cmd.Flags().GetString("expires")
			prof, err := a.profile(cmd)
			if err != nil {
				return err
			}
			prev, _ := config.Load(prof)
			// A development build may create a new profile, but must not log in to
			// the default profile or to an existing profile that did not opt in.
			if !profile.IsRelease(a.Version) && (prof == config.DefaultProfile || prev != nil) {
				if err := a.checkDevBuild(prof, prev); err != nil {
					return err
				}
			}
			if site == "" {
				if site, err = a.TTY.Prompt("Atlassian site (e.g. your-team.atlassian.net): "); err != nil {
					return err
				}
			}
			if site, err = profile.NormalizeSite(site); err != nil {
				return err
			}
			if email == "" {
				if email, err = a.TTY.Prompt("Account email: "); err != nil {
					return err
				}
			}
			var token string
			if withToken, _ := cmd.Flags().GetBool("with-token"); withToken {
				line, err := bufio.NewReader(io.LimitReader(a.In, 4096)).ReadString('\n')
				if err != nil && !errors.Is(err, io.EOF) {
					return err
				}
				token = strings.TrimSpace(line)
			} else if token, err = a.TTY.Secret("API token (input hidden): "); err != nil {
				return err
			}
			if token == "" {
				return errors.New("no token entered")
			}
			email = strings.TrimSpace(email)
			me, cloudID, err := a.verifyToken(cmd.Context(), site, email, token)
			if err != nil {
				return loginError(site, email, err)
			}
			if !cmd.Flags().Changed("expires") {
				if expires, err = a.TTY.Prompt("Token expiry date shown by Atlassian (YYYY-MM-DD, empty to skip): "); err != nil {
					return err
				}
			}
			cfg := &config.Config{Account: profile.Account{Site: site, Email: email, TokenExpiry: strings.TrimSpace(expires),
				ScopedToken: cloudID != "", CloudID: cloudID}, Privacy: config.PrivacyOwn}
			if prev != nil {
				cfg.Privacy = prev.Privacy
				cfg.AllowDevBuilds = prev.AllowDevBuilds && prev.Site == cfg.Site
				if prev.Site == cfg.Site {
					cfg.Spaces, cfg.Parents = prev.Spaces, prev.Parents
				}
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			if err := a.Secrets.Set(keychain.AccountKey(cfg.Site, cfg.Email), token); err != nil {
				return err
			}
			if prev != nil && (prev.Site != cfg.Site || prev.Email != cfg.Email) {
				_ = a.Secrets.Delete(keychain.AccountKey(prev.Site, prev.Email))
			}
			if err := cfg.Save(prof); err != nil {
				return err
			}
			kind := "classic token"
			if cfg.ScopedToken {
				kind = "scoped token, via the Atlassian API gateway"
			}
			_, err = fmt.Fprintf(a.Out, "Logged in as %s on %s (profile %s, %s). Privacy mode: %s.\n", me.DisplayName, cfg.Site, prof, kind, cfg.Privacy)
			if err == nil && len(cfg.Spaces) == 0 {
				_, err = fmt.Fprintln(a.Out, "Next, allow the spaces dok may use: `dok config spaces add KEY`.")
			}
			return err
		},
	}
	login.Flags().String("site", "", "Atlassian site, e.g. your-team.atlassian.net")
	login.Flags().String("email", "", "your Atlassian account email")
	login.Flags().String("expires", "", "token expiry date (YYYY-MM-DD), to be warned in time")
	login.Flags().Bool("with-token", false, "read the API token from stdin instead of prompting, e.g. from a password manager")

	status := &cobra.Command{
		Use:   "status",
		Short: "Check that the stored credentials work",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, cfg, err := a.service(cmd)
			if err != nil {
				return err
			}
			me, err := svc.Whoami(cmd.Context())
			if err != nil {
				return err
			}
			expiry := "not recorded"
			if cfg.TokenExpiry != "" {
				expiry = cfg.TokenExpiry
			}
			prof, _ := a.profile(cmd)
			_, err = fmt.Fprintf(a.Out, "Profile: %s.\nLogged in as %s on %s.\nPrivacy mode: %s.\nToken expiry: %s.\n", prof, me.DisplayName, me.Site, me.Privacy, expiry)
			return err
		},
	}

	logout := &cobra.Command{
		Use:   "logout",
		Short: "Remove the stored API token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			prof, err := a.profile(cmd)
			if err != nil {
				return err
			}
			cfg, err := config.Load(prof)
			if err != nil {
				return err
			}
			if err := confirm(a.TTY, fmt.Sprintf("Remove the API token for %s on %s (profile %s) from the Keychain?", cfg.Email, cfg.Site, prof), "", false); err != nil {
				return err
			}
			if err := a.Secrets.Delete(keychain.AccountKey(cfg.Site, cfg.Email)); err != nil {
				return err
			}
			_, err = fmt.Fprintln(a.Out, "Token removed. Revoke it at https://id.atlassian.com/manage-profile/security/api-tokens if you no longer need it.")
			return err
		},
	}
	cmd.AddCommand(login, status, logout)
	return cmd
}

// errLoginRejected is returned when Confluence does not accept the email and token.
var errLoginRejected = errors.New("this email and API token were not accepted by Confluence")

// loginError explains a failed credential check at login. Nothing has been
// stored at that point.
func loginError(site, email string, err error) error {
	switch {
	case errors.Is(err, atlassian.ErrUnauthorized):
		return fmt.Errorf("%w on %s; nothing was saved. Check that you pasted the API token itself (not your password), "+
			"that it was created for the Atlassian account %s, and that it has not expired or been revoked: "+
			"https://id.atlassian.com/manage-profile/security/api-tokens", errLoginRejected, site, email)
	case errors.Is(err, atlassian.ErrNotFound):
		return fmt.Errorf("%s does not look like an Atlassian Cloud site with Confluence (no REST API found); nothing was saved", site)
	}
	return fmt.Errorf("could not check the credentials with %s; nothing was saved: %w", site, err)
}

func (a *App) configCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Show or change settings"}
	show := &cobra.Command{
		Use:   "show",
		Short: "Show the settings (the token is never shown)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			prof, err := a.profile(cmd)
			if err != nil {
				return err
			}
			cfg, err := config.Load(prof)
			if err != nil {
				return err
			}
			path, _ := config.Path(prof)
			return a.print(cmd, cfg, func() string {
				token := "classic"
				if cfg.ScopedToken {
					token = "scoped (via the Atlassian API gateway, cloud ID " + cfg.CloudID + ")"
				}
				parents := "anywhere in the spaces"
				if len(cfg.Parents) > 0 {
					parents = "only below pages " + strings.Join(cfg.Parents, ", ")
				}
				return fmt.Sprintf("- Profile: %s\n- Site: %s\n- Email: %s\n- Token: %s\n- Privacy: %s\n- Spaces: %s\n- Writes: %s\n- Token expiry: %s\n- Development builds: %v\n- File: %s\n",
					prof, cfg.Site, cfg.Email, token, cfg.Privacy, orDash(strings.Join(cfg.Spaces, ", ")), parents, orDash(cfg.TokenExpiry), cfg.AllowDevBuilds, path)
			})
		},
	}
	privacy := &cobra.Command{
		Use:   "privacy own|off",
		Short: "Show other people as labels (own) or by name (off)",
		Long: "own: other people appear as \"Person A\", \"Person B\", ...; you are \"Me\".\n" +
			"off: people appear by name.\n" +
			"In both modes only the allowed spaces are visible (see `dok config spaces`).\n" +
			"Switching to off must be confirmed in the terminal. Restart MCP clients afterwards.",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"own", "off"},
		RunE: func(cmd *cobra.Command, args []string) error {
			prof, err := a.profile(cmd)
			if err != nil {
				return err
			}
			cfg, err := config.Load(prof)
			if err != nil {
				return err
			}
			mode := config.PrivacyMode(strings.ToLower(args[0]))
			if mode != config.PrivacyOwn && mode != config.PrivacyOff {
				return fmt.Errorf("unknown mode %q; use own or off", args[0])
			}
			if mode == config.PrivacyOff && cfg.Privacy != config.PrivacyOff {
				answer, err := a.TTY.Prompt(target(cfg.Site, prof) + "\n\nPrivacy off shows assistants the real names of everyone in the allowed spaces.\nType off to confirm: ")
				if err != nil {
					return err
				}
				if answer != "off" {
					return errors.New("not confirmed; privacy mode unchanged")
				}
			}
			cfg.Privacy = mode
			if err := cfg.Save(prof); err != nil {
				return err
			}
			_, err = fmt.Fprintf(a.Out, "Privacy mode: %s. Restart MCP clients to apply it.\n", mode)
			return err
		},
	}
	profiles := &cobra.Command{
		Use:   "profiles",
		Short: "List configured profiles",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			current, err := a.profile(cmd)
			if err != nil {
				return err
			}
			names, err := config.Profiles()
			if err != nil {
				return err
			}
			if len(names) == 0 {
				_, err := fmt.Fprintln(a.Out, "No profiles yet; run `dok auth login`.")
				return err
			}
			for _, n := range names {
				mark := " "
				if n == current {
					mark = "*"
				}
				line := fmt.Sprintf("%s %s", mark, n)
				if cfg, err := config.Load(n); err == nil {
					line += fmt.Sprintf(": %s (%s, privacy %s)", cfg.Site, cfg.Email, cfg.Privacy)
					if cfg.AllowDevBuilds {
						line += " [dev builds allowed]"
					}
				}
				if _, err := fmt.Fprintln(a.Out, line); err != nil {
					return err
				}
			}
			return nil
		},
	}
	devBuilds := &cobra.Command{
		Use:   "dev-builds on|off",
		Short: "Allow development builds and integration tests to use this profile",
		Long: "Development builds of dok (built from a checkout, e.g. dok-dev) refuse every profile unless it opted in here.\n" +
			"Only opt in profiles for test sites. The default profile cannot opt in. Switching on must be confirmed in the terminal.",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"on", "off"},
		RunE: func(cmd *cobra.Command, args []string) error {
			p, _ := cmd.Flags().GetString("profile")
			if p == "" {
				return errors.New("name the profile explicitly with --profile")
			}
			prof, err := config.ResolveProfile(p)
			if err != nil {
				return err
			}
			cfg, err := config.Load(prof)
			if err != nil {
				return err
			}
			switch strings.ToLower(args[0]) {
			case "on":
				if prof == config.DefaultProfile {
					return errors.New("the default profile is meant for production and cannot allow development builds")
				}
				host := strings.TrimPrefix(cfg.Site, "https://")
				answer, err := a.TTY.Prompt(target(cfg.Site, prof) + "\n\nDevelopment builds and integration tests will be able to read and change pages on this site.\nOnly do this for a test site. Type " + host + " to confirm: ")
				if err != nil {
					return err
				}
				if answer != host {
					return errors.New("not confirmed; nothing was changed")
				}
				cfg.AllowDevBuilds = true
			case "off":
				cfg.AllowDevBuilds = false
			default:
				return fmt.Errorf("use on or off, not %q", args[0])
			}
			if err := cfg.Save(prof); err != nil {
				return err
			}
			_, err = fmt.Fprintf(a.Out, "Development builds on profile %s: %v.\n", prof, cfg.AllowDevBuilds)
			return err
		},
	}
	cmd.AddCommand(show, privacy, profiles, devBuilds, a.spacesCmd(), a.parentsCmd())
	return cmd
}

// saveConfig loads the selected profile, changes it and saves it.
func (a *App) saveConfig(cmd *cobra.Command, change func(*config.Config) (string, error)) error {
	prof, err := a.profile(cmd)
	if err != nil {
		return err
	}
	cfg, err := config.Load(prof)
	if err != nil {
		return err
	}
	msg, err := change(cfg)
	if err != nil {
		return err
	}
	if err := cfg.Save(prof); err != nil {
		return err
	}
	_, err = fmt.Fprintln(a.Out, msg+" Restart MCP clients to apply it.")
	return err
}

func (a *App) spacesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spaces",
		Short: "Choose the spaces dok may use",
		Long:  "dok only sees and changes pages in the spaces listed here. Everything else on the site stays invisible.",
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List the allowed spaces",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, _, err := a.service(cmd)
			if err != nil {
				return err
			}
			ss := svc.Spaces()
			return a.print(cmd, ss, func() string { return render.Spaces(ss) })
		},
	}
	add := &cobra.Command{
		Use:   "add KEY...",
		Short: "Allow spaces, by key",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.saveConfig(cmd, func(cfg *config.Config) (string, error) {
				for _, k := range args {
					key, err := config.NormalizeSpaceKey(k)
					if err != nil {
						return "", err
					}
					cfg.AddSpace(key)
				}
				return "Allowed spaces: " + strings.Join(cfg.Spaces, ", ") + ".", nil
			})
		},
	}
	remove := &cobra.Command{
		Use:   "remove KEY...",
		Short: "Stop allowing spaces",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.saveConfig(cmd, func(cfg *config.Config) (string, error) {
				for _, k := range args {
					key, err := config.NormalizeSpaceKey(k)
					if err != nil {
						return "", err
					}
					if !cfg.RemoveSpace(key) {
						return "", fmt.Errorf("space %s is not allowed", key)
					}
				}
				return "Allowed spaces: " + orDash(strings.Join(cfg.Spaces, ", ")) + ".", nil
			})
		},
	}
	cmd.AddCommand(list, add, remove)
	return cmd
}

func (a *App) parentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "parents",
		Short: "Limit writes to the trees below certain pages",
		Long:  "With parent pages set, dok only creates, changes and comments on those pages and pages below them. Reading is not affected.",
	}
	add := &cobra.Command{
		Use:   "add PAGE...",
		Short: "Allow writes below pages, by ID or URL",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.saveConfig(cmd, func(cfg *config.Config) (string, error) {
				for _, ref := range args {
					id, err := confluence.PageID(ref)
					if err != nil {
						return "", err
					}
					if !slices.Contains(cfg.Parents, id) {
						cfg.Parents = append(cfg.Parents, id)
					}
				}
				return "Writes only below pages: " + strings.Join(cfg.Parents, ", ") + ".", nil
			})
		},
	}
	clear := &cobra.Command{
		Use:   "clear",
		Short: "Allow writes anywhere in the allowed spaces again",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.saveConfig(cmd, func(cfg *config.Config) (string, error) {
				cfg.Parents = nil
				return "Writes allowed anywhere in the allowed spaces.", nil
			})
		},
	}
	cmd.AddCommand(add, clear)
	return cmd
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// verifyToken checks the token against the site and, when the site rejects
// it, through the API gateway, which is where scoped tokens work. cloudID is
// empty for a classic token.
func (a *App) verifyToken(ctx context.Context, site, email, token string) (*confluence.User, string, error) {
	me, err := a.client(site, email, token, "").Myself(ctx)
	if !errors.Is(err, atlassian.ErrUnauthorized) {
		return me, "", err
	}
	siteErr := err
	cloudID, err := a.client(site, email, token, "").CloudID(ctx)
	if err != nil {
		return nil, "", siteErr
	}
	me, err = a.client(site, email, token, cloudID).Myself(ctx)
	switch {
	case errors.Is(err, atlassian.ErrMissingScope):
		return nil, "", fmt.Errorf("the scoped token is valid but lacks read:confluence-user, which dok needs to identify you: %w", err)
	case err != nil:
		return nil, "", siteErr
	}
	return me, cloudID, nil
}
