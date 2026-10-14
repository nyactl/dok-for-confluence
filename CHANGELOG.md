# Changelog

All notable changes are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/).

## Unreleased

### Added

- Pages read and write expands, column layouts, smart links, colored text and images as Markdown forms, and keep everything else, such as panels, status lozenges or macros, as placeholders that name the part without its content. An update keeps or removes them, and the approval lists the parts added and removed.
- Macros: `dok config macros allow` for the table of contents, child pages and whiteboard embeds, `learn` for any macro inserted on a page in the editor, and `fence` to turn fenced code such as `mermaid` into a diagram app's macro. Pages show allowed macros as ```` ```macro:NAME ```` blocks; MCP `list_macros` lists them. Macros that include content from elsewhere or embed code are never created.
- `--attach` on `dok create` and `dok update`, and `attach` on MCP `create_page` and `update_page`, upload files first for images that name them; new pages with images are created as drafts, filled once the files are attached.

### Changed

- Updates no longer refuse parts without a Markdown form; they are kept through placeholders.

## [0.1.3] - 2026-10-13

### Fixed

- Updates of pages with empty paragraphs or headings, which the Confluence editor leaves, are no longer refused.

## [0.1.2] - 2026-10-13

### Fixed

- Updates no longer delete parts of a page that have no Markdown form, such as app macros, images, layouts, expands and status lozenges: such updates are refused and name the parts, and reading a page marks where macros are. A title change alone keeps the content as it is.

## [0.1.1] - 2026-10-13

### Fixed

- Space keys match regardless of case, as in Confluence: keys are no longer turned into upper case, so spaces with mixed-case keys can be allowed, and keys configured in other case still find their space.

## [0.1.0] - 2026-10-13

### Added

- Read pages as Markdown, list child pages, comments and attachments, and search the allowed spaces, from the CLI and as MCP tools.
- Create pages (drafts unless published right away), publish drafts, update pages with a version check and a diff, add comments with mentions, and attach files; every change needs approval in the terminal or an MCP approval dialog.
- Space allowlist (`dok config spaces`) and optional write limits to page trees (`dok config parents`).
- Privacy mode `own`: other people appear as "Person A", "Person B".
- Scoped API tokens through the Atlassian API gateway, with the missing scope named in errors.
- Profiles, and development builds that only use profiles that opted in.

[0.1.3]: https://github.com/nyactl/dok-for-confluence/releases/tag/v0.1.3
[0.1.2]: https://github.com/nyactl/dok-for-confluence/releases/tag/v0.1.2
[0.1.1]: https://github.com/nyactl/dok-for-confluence/releases/tag/v0.1.1
[0.1.0]: https://github.com/nyactl/dok-for-confluence/releases/tag/v0.1.0
