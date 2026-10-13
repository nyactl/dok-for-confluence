# Design

```
cmd/dok: main.go
 └─ internal/cli         cobra commands, terminal confirmation
 └─ internal/mcpserver   MCP tools; approval through approvekit/approval
     └─ internal/core    operations: allowlist, redaction, write plans, diffs
         ├─ internal/confluence  Confluence endpoints on approvekit/atlassian
         ├─ internal/render      views -> Markdown / JSON
         ├─ go-adf               Atlassian Document Format <-> Markdown
         └─ approvekit/people    labels for people, redaction
 internal/config          settings per profile on approvekit/profile
 internal/confluencetest  in-memory fake Confluence Cloud for tests
```

## Scope

- **Allowlist.** The profile lists space keys. At start, dok resolves them to space IDs; every page read checks the page's space ID, and search adds `space in (...)` to the CQL and checks each result again. Pages elsewhere are reported exactly like missing pages.
- **Write trees.** With parent pages set, a write is allowed only on those pages or pages below them; dok follows the `parentId` chain upwards (at most 100 levels), which needs no extra token scope.

## People

Authors, editors, commenters and mentioned people are looked up once by account ID and registered with approvekit's `people`, which hands out labels ("Person A") in mode `own`. Names are redacted in each ADF text piece before conversion and again in the Markdown. Search asks Confluence for each hit's creator and last editor, so their names are known before titles and excerpts are shown.

## Writes

Every write is a `Plan` with an exact description, applied once after approval.

- **Create** makes a draft unless publishing is requested. A draft is readable as `status: draft`.
- **Publish** sends `status: current` with the draft's own version number, which is what Confluence expects.
- **Update** needs the version the change is based on. Confluence itself requires the next version number and answers 409 otherwise; dok checks first, shows a line diff of the Markdown, and refuses updates without changes (Confluence would accept them without creating a version).
- **Comment** writes a footer comment; `@[label]` mentions resolve only to people shown in this session.
- **Attach** reads the file when the plan is made, so the upload is what was approved; the MCP tool refuses relative paths and hidden files and folders.

## API

Requests go to the site with a classic token, or through `https://api.atlassian.com/ex/confluence/<cloudId>` with a scoped token. dok uses the v2 API for spaces, pages, children, comments and attachments, and the v1 API for users, search and uploads. The scopes per endpoint are taken from Atlassian's API specifications and named in errors.
