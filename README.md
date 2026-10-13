# dok-for-confluence

[![CI](https://github.com/nyactl/dok-for-confluence/actions/workflows/ci.yml/badge.svg)](https://github.com/nyactl/dok-for-confluence/actions/workflows/ci.yml)

Read and write **Confluence Cloud** pages from the terminal, or let an assistant do it as an **MCP server**, while you stay in control:

- **Only the spaces you allow.** Everything else on the site is invisible, including search results and child pages in other spaces.
- **Every change approved by you**: in the terminal, or in an approval dialog of your MCP client that the model cannot answer. New pages are drafts until you publish them, and an update fails if the page changed since it was read, so nothing is overwritten unseen.
- **Other people stay private** (privacy mode `own`): authors, editors, commenters and mentioned people appear as "Person A", "Person B", and you as "Me". Their names are also replaced in page text where dok knows them.
- **Markdown in, Markdown out**, converted with [go-adf](https://github.com/nyactl/go-adf), which always writes valid Atlassian Document Format.
- **The API token stays in the macOS Keychain** and only goes to your site or Atlassian's API gateway.

The command is `dok`. Its companion for Jira is [tix-for-jira](https://github.com/nyactl/tix-for-jira); both build on [approvekit](https://github.com/nyactl/approvekit).

Not affiliated with or endorsed by Atlassian. Confluence, Jira and Atlassian are trademarks of Atlassian.

## Install

```sh
brew install nyactl/tap/dok-for-confluence
```

Or with Go 1.26 or newer (macOS, cgo for the Keychain):

```sh
go install github.com/nyactl/dok-for-confluence/cmd/dok@latest
```

## Set up

1. Create an API token at <https://id.atlassian.com/manage-profile/security/api-tokens>, preferably a scoped one for the app **Confluence** (see [Scoped API tokens](#scoped-api-tokens)). Note its expiry date.
2. Log in:

   ```sh
   dok auth login
   ```

   dok asks for your site (e.g. `your-team.atlassian.net`), email, token (hidden) and the token's expiry date, checks them and stores the token in the Keychain. To pass the token from a password manager, pipe it in with `--with-token` (together with `--site`, `--email` and `--expires`).

3. Allow the spaces dok may use, by key (shown in the space's URL: `/wiki/spaces/KEY/...`):

   ```sh
   dok config spaces add DOCS TEAM
   ```

4. Optionally limit writes to certain page trees: `dok config parents add <page ID or URL>`. Reading stays possible in the whole space.

Check with `dok whoami`.

### Scoped API tokens

A classic API token can do everything your account can do, in every Atlassian product. A scoped token is limited to the scopes you pick, so a leaked token exposes less, and a token without write scopes makes Confluence itself refuse changes. dok recognises a scoped token at login and sends all requests through Atlassian's API gateway, the only place scoped tokens work.

| Use | Scopes |
|---|---|
| Read | `read:confluence-user`, `read:space:confluence`, `read:page:confluence`, `read:hierarchical-content:confluence`, `read:comment:confluence`, `read:attachment:confluence`, `search:confluence` |
| Also write | add `write:page:confluence`, `write:comment:confluence`, `write:confluence-file` |

If a scope is missing, dok names the scope the failed request needs.

## Use from the terminal

```sh
dok spaces                                   # the allowed spaces
dok search "release checklist"               # pages in the allowed spaces, newest first
dok get 123456                               # a page as Markdown, with its version
dok get https://your-team.atlassian.net/wiki/spaces/DOCS/pages/123456/Runbook
dok children 123456
dok comments 123456

dok create --parent 123456 --title "Design notes" --file notes.md   # a draft
dok publish 234567
dok update 123456 --version 7 --file runbook.md --message "Add rollback steps"
dok comment 123456 --body "Updated the rollback steps, @[Person A]"
dok attach 123456 ./diagram.svg
```

Every change shows exactly what will happen, a line diff for updates, and asks for confirmation. Parts of a page that have no Markdown form, such as app macros, images, layouts, expands or status lozenges, are protected: macros show up as `(macro "Title" not shown)` when reading, and an update that would delete any of these parts is refused with their names, so change such pages in Confluence. A title change alone keeps the content as it is. Updates and publishing need the page ID typed; other changes a `y`. Add `--json` to read commands for machine-readable output.

## Use with an MCP client

Register dok as a stdio MCP server in your client's configuration, for example:

```json
{
  "mcpServers": {
    "dok-for-confluence": {
      "command": "dok",
      "args": ["mcp", "--allow-writes"]
    }
  }
}
```

No credentials go into the client configuration; dok reads them from its own config and the Keychain. Without `--allow-writes` the server only offers read tools. Do not configure auto-approval or elicitation hooks for this server; they would answer on your behalf.

| Read tools | Write tools (with `--allow-writes`) |
|---|---|
| `whoami`, `list_spaces`, `get_page`, `list_children`, `search_pages`, `list_comments`, `list_attachments` | `create_page`, `update_page`, `publish_page`, `add_comment`, `attach_file` |

Each write opens an approval dialog in the client, through MCP elicitation, with the exact change. Approvals are single-use, expire after 10 minutes and are bound to the change shown. Clients that cannot show such dialogs cannot write. `attach_file` only accepts absolute paths and refuses hidden files and folders.

## Privacy modes

- `own` (default): other people appear as labels, you as "Me". Names of people dok has seen are also replaced in free text, best effort.
- `off`: people appear by name. Switch with `dok config privacy off`, confirmed in the terminal.

In both modes, only the allowed spaces are visible.

## Profiles: test and production

Profiles keep sites apart: `dok --profile test ...` or `DOK_PROFILE=test`. The default profile is meant for production. Builds from a checkout (e.g. `make dev`, which installs `dok-dev`) refuse to run without an explicit profile, and only use profiles that opted in with `dok config dev-builds on --profile NAME`; the default profile never can.

## Limitations

- Page bodies are converted to and from Markdown. Confluence features without a Markdown form (macros, layouts, panels, status lozenges) are read as text, and a page written with dok contains only what Markdown can express. Updating a page with such features replaces them; the diff shows it before you approve.
- Mermaid diagrams are written as code blocks; rendering them is planned.
- dok cannot delete pages, change page permissions or download attachments.
- macOS only for now, because of the Keychain.

## Development

```sh
make test   # unit tests against an in-memory fake Confluence
make lint
make dev    # installs dok-dev
make it     # integration tests against a test site, see integration/README.md
```

See [CONTRIBUTING.md](CONTRIBUTING.md) and [docs/design.md](docs/design.md). Security problems go through [SECURITY.md](SECURITY.md).

## License

MIT, see [LICENSE](LICENSE).
