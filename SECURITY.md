# Security policy

## Supported versions

Only the latest release receives fixes.

## Reporting a vulnerability

Please do not open a public issue. Report privately through the repository's **Security** tab → **Report a vulnerability**, with the version or commit, steps to reproduce and the impact you expect.

You will get an acknowledgement within 7 days. Fixes are released before details are published, and reporters are credited unless they prefer otherwise.

## What dok promises

Reports that break any of these are in scope:

1. **Allowlist:** no page, child page, comment, attachment or search result from a space outside the allowed spaces reaches the output, and nothing outside them is changed. With parent pages set, nothing outside those page trees is changed.
2. **Redaction:** in mode `own`, other people's account IDs, emails and display names from structured data never reach the output; names and emails of people dok has seen are also replaced in page text.
3. **Consent:** no write happens without an approval given by the human, in the terminal (CLI) or in an elicitation dialog (MCP). Approvals are single-use, expire after 10 minutes and are bound to the exact change shown. An update fails if the page changed after the version it is based on.
4. **Containment:** the API token is only sent to the configured `https` site or Atlassian's API gateway; redirects to another host are refused; the token is stored only in the macOS Keychain.
5. **Output safety:** terminal control sequences, bidirectional overrides and invisible characters from Confluence are removed before output.
6. **Development builds** never use the default profile or a profile that did not opt in.
7. **Page parts:** parts of a page without a Markdown form reach the output only as placeholders naming their kind, without their content; an update can keep or remove them but not add or change them, and other JSON is refused. Only macros the profile allows are created, never ones that include content from elsewhere or embed code, and a learned definition holds no page text and no people.

Known limits, not vulnerabilities: names typed in free text by people dok has not seen in this session; the contents of attachments; a program that controls a pseudo-terminal can type a CLI confirmation; MCP clients configured to answer elicitations automatically.
