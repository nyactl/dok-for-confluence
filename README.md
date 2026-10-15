# dok-for-confluence

[![CI](https://github.com/nyactl/dok-for-confluence/actions/workflows/ci.yml/badge.svg)](https://github.com/nyactl/dok-for-confluence/actions/workflows/ci.yml)

Read and write **Confluence Cloud** pages from the terminal, or let an assistant do it as an **MCP server**, while you stay in control:

- **Only the spaces you allow.** Everything else on the site is invisible, including search results and child pages in other spaces.
- **Every change approved by you**: in the terminal, or in an approval dialog of your MCP client that the model cannot answer. New pages are drafts until you publish them, and an update fails if the page changed since it was read, so nothing is overwritten unseen.
- **Other people stay private** (privacy mode `own`): authors, editors, commenters and mentioned people appear as "Person A", "Person B", and you as "Me". Their names are also replaced in page text where dok knows them.
- **Markdown in, Markdown out**, converted with [go-adf](https://github.com/nyactl/go-adf), which always writes valid Atlassian Document Format.
- **The API token stays in the macOS Keychain** and only goes to your site or Atlassian's API gateway.

The command is `dok`. Its companion for Jira is [tix-for-jira](https://github.com/nyactl/tix-for-jira); both build on [approvekit](https://github.com/nyactl/approvekit).

![dok reads a page with an expand, a table of contents macro and placeholders, and shows the diff before an update](docs/demo.gif)

Not affiliated with or endorsed by Atlassian. Confluence, Jira and Atlassian are trademarks of Atlassian.

## dok-for-confluence or Atlassian's Rovo MCP server?

Atlassian offers its own hosted [Rovo MCP server](https://developer.atlassian.com/cloud/rovo-mcp/) for Jira, Confluence and other Atlassian apps. It is the broader tool; dok-for-confluence is the narrower, stricter one. Both can be used side by side.

| | dok-for-confluence | Rovo MCP server |
|---|---|---|
| What the assistant sees | the spaces you allowed; writes optionally only below certain pages | everything your account can see |
| Other people | "Person A", "Person B"; no names, emails or account IDs | as stored in Confluence |
| Writes | each one approved by you in a dialog the assistant cannot answer, single-use and bound to the shown change; new pages are drafts | as your AI client handles tool calls |
| Macros and page parts | only macros you allowed; other parts kept as placeholders, never shown raw or invented | as written by the assistant |
| Runs | locally; the token stays in your system keychain | hosted by Atlassian, OAuth or API token |
| Covers | Confluence; for Jira tickets, its companion [tix-for-jira](https://github.com/nyactl/tix-for-jira) works the same way | Jira, Confluence, Jira Service Management, Bitbucket, Loom and more, with semantic search |
| Cost | free, open source | some tools use Rovo credits |

Use dok-for-confluence when colleagues' data and other spaces must not reach the assistant, or when nothing should change in Confluence without your explicit approval. Use Rovo for broad research across Atlassian apps where that is acceptable.

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
dok create --parent 123456 --title "Architecture" --file arch.md --attach ./diagram.png
dok publish 234567
dok update 123456 --version 7 --file runbook.md --message "Add rollback steps"
dok comment 123456 --body "Updated the rollback steps, @[Person A]"
dok attach 123456 ./diagram.svg
```

**Choosing instead of typing:** leave out the page, as in `dok get`, `dok comment --body "…"` or `dok update --version 7 --file page.md`, and a picker lists the pages of the allowed spaces, most recently changed first: type part of the title, move with the arrow keys, press Enter.

![dok get without a page: choose it by typing part of its title](docs/pick.gif)

**Tab completion:** `dok completion zsh` (or `bash`, `fish`, `powershell`) prints a completion script; see `dok completion zsh --help`. Page IDs complete with their titles, and macro names in `dok config macros remove`; with a plugin such as [fzf-tab](https://github.com/Aloxaf/fzf-tab), completions show in fzf.

**Mentions:** `@[Person A]` mentions the person shown as Person A. Labels stay the same for a profile across commands and MCP sessions, kept in a file next to the profile's config that holds only account IDs and labels.

Every change shows exactly what will happen: a line diff for updates, the macros, images and other parts it adds or removes, and the files it attaches first. Every change asks `Apply this change? [y/N]`; only `y` applies it, and just pressing Enter cancels. Add `--json` to read commands for machine-readable output.

## Pages as Markdown

Pages read and write as CommonMark, with forms for what the Confluence editor adds:

`````markdown
<details>
<summary>Rollback steps</summary>

1. Stop the service

</details>

````columns
```column 50
Left
```

```column 50
Right
```
````

See <card:https://your-team.atlassian.net/browse/PROJ-1>, now {color:#006644}green{/color}.

![diagram.png](attachment:diagram.png "center 600px")

```macro:toc
maxLevel: 2
```
`````

Images refer to the page's attachments by file name; `--attach` uploads new files first, in the same approved change. Everything without a form, such as macros that are not allowed, panels, status lozenges or Jira lists, appears as a placeholder:

````markdown
```adf
{"type":"kept","attrs":{"id":"k3-1a2b3c","what":"panel"}}
```
````

A placeholder names the part but not its content, which can hold people. Keep it to keep the part, or remove it to delete the part; the approval names what is removed. Placeholders cannot be invented or copied between pages, and any other JSON is refused.

## Macros

dok creates only the macros a profile allows, as ```` ```macro:NAME ```` blocks of `name: value` lines, each value checked:

```sh
dok config macros allow toc children whiteboard   # built in; whiteboards from allowed spaces only
dok config macros learn 123456                    # macros inserted on that page in the editor, e.g. an app's
dok config macros fence mermaid mermaid-diagram source.text   # ```mermaid code becomes that macro
dok config macros list
```

`learn` works for any app, whether or not dok knows it: it keeps the macro's identity and settings, never the page's text or people, and each macro is confirmed in the terminal. Macros that show content from elsewhere (include page, excerpt include and the like) or embed HTML, iframes or scripts are never created, even if listed by hand. Assistants see the allowed macros with `list_macros`.

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
| `whoami`, `list_spaces`, `get_page`, `list_children`, `search_pages`, `list_comments`, `list_attachments`, `list_macros` | `create_page`, `update_page`, `publish_page`, `add_comment`, `attach_file` |

Each write opens an approval dialog in the client, through MCP elicitation, with the exact change. Approvals are single-use, expire after 10 minutes and are bound to the change shown. Clients that cannot show such dialogs cannot write. `attach_file`, and `attach` on `create_page` and `update_page`, only accept absolute paths and refuse hidden files and folders.

## Privacy modes

- `own` (default): other people appear as labels, you as "Me". Names of people dok has seen are also replaced in free text, best effort.
- `off`: people appear by name. Switch with `dok config privacy off`, confirmed in the terminal.

In both modes, only the allowed spaces are visible.

## Profiles: test and production

Profiles keep sites apart: `dok --profile test ...` or `DOK_PROFILE=test`. The default profile is meant for production. Builds from a checkout (e.g. `make dev`, which installs `dok-dev`) refuse to run without an explicit profile, and only use profiles that opted in with `dok config dev-builds on --profile NAME`; the default profile never can.

## Limitations

- Parts without a Markdown form are kept as placeholders, but cannot be changed through dok; edit them in Confluence. Attributes of parts that do have a form, such as table column widths, are not kept.
- Macros with a body (such as page properties) can be kept, not created.
- Mermaid diagrams render only through a diagram app installed on the site, mapped with `dok config macros fence`; otherwise they stay code blocks.
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
