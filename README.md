# mdoc

`mdoc` publishes Markdown as native Google Docs. Markdown stays canonical. The default publish mode creates immutable review generations. It does not replace or delete active review content. For version 3 publications, mdoc can also read supported review edits from Google Docs and produce a patch for the Markdown sources.

## Requirements

- macOS on Apple silicon
- Pandoc 3.1 or newer and below 4.0
- librsvg when a selected publication contains SVG images
- a Google Cloud desktop OAuth client
- My Drive access through the `drive.file` scope

Install the local render tools with `brew install pandoc librsvg`. Validation and doctor test `rsvg-convert` before publish when a selected publication contains SVG images.

## Install

Verify and extract the release archive:

```sh
shasum -a 256 -c SHA256SUMS
tar -xzf mdoc-v0.1.0-darwin-arm64.tar.gz
cd mdoc-v0.1.0-darwin-arm64
shasum -a 256 -c SHA256SUMS
sudo install -m 0755 bin/mdoc /usr/local/bin/mdoc
mdoc --version
```

The archive contains the binary, a neutral example config, and the optional built-in title filter. It does not require a repository reference DOCX.

## Create a project

Run this from the project root:

```sh
mdoc init --profile default
```

This creates `.mdoc.yaml` with a unique project ID and neutral defaults. The packaged `share/mdoc/mdoc.yaml.example` shows all profile fields. Do not copy its example project ID into real projects.

When `--config` is absent, mdoc searches the current directory and each parent for the nearest `.mdoc.yaml`. An explicit `--config` disables that search.

Profile selection uses this order:

1. `--profile`
2. `MDOC_PROFILE`
3. project `default_profile`
4. global `default_profile`
5. the only profile in the project

Use these local-only commands to manage profiles:

```sh
mdoc profile add testing
mdoc profile list
mdoc profile show testing
```

## Configuration

Effective values use this order, from lowest to highest priority:

1. built-in defaults
2. global config
3. project config
4. environment
5. explicit command flags

The optional global config is at `<UserConfigDir>/mdoc/config.yaml`. On macOS this is under the user Library application support area. A small global config can set defaults such as:

```yaml
version: 1
default_profile: default
pandoc_binary: pandoc
output:
  format: human
  quiet: false
profile_defaults:
  style:
    reader: gfm
```

Supported config environment overrides include `MDOC_PROFILE`, `MDOC_ROOT`, `MDOC_PANDOC_BINARY`, `MDOC_OUTPUT_FORMAT`, `MDOC_OUTPUT_QUIET`, `MDOC_ENTRY`, `MDOC_REFERENCE_DOCX`, `MDOC_STATE_FILE`, `MDOC_MAX_TABLE_COLUMNS`, `MDOC_SOURCES`, `MDOC_EXCLUDES`, and `MDOC_FILTERS`. List values use JSON array syntax.

Only flags present in the command line override config. Use `mdoc config show` to inspect the effective redacted values and their origins.

### Config versions and publications

Version 1 and version 2 configs keep their source based behavior and JSON schema version 1. Version 3 makes a publication the unit of planning, state, rendering, and publishing. A source publication renders one discovered source. A bundle publication renders an ordered list of sources into one DOCX and one native Google Doc.

```yaml
version: 3
project_id: example-project
profiles:
  review:
    sources:
      include: [docs/*.md]
    entry: handbook
    publications:
      handbook:
        kind: bundle
        title: Engineering Handbook
        review:
          pull:
            enabled: true
        members:
          - source: docs/overview.md
            start: after_cover
          - source: docs/operations.md
            start: new_page
        layout:
          cover:
            enabled: true
            title: '{{ field "product" }} Handbook'
          table_of_contents:
            enabled: true
            mode: static
            title: Contents
            depth: 1
      overview:
        kind: source
        source: docs/overview.md
```

Version 3 target keys are `publication:<publication-id>`. A source path change does not change this identity. Changing a publication ID creates a new target and leaves the old target as orphan history. Two source publications may use the same source without sharing state.

### Fields and templates

Field values use this order, from lowest to highest priority:

1. profile `field_defaults`
2. publication `field_files`, in listed order
3. publication `fields`
4. repeated `--fields-file` values, in command order
5. repeated `--field` assignments, in command order

Maps merge recursively. Scalars and lists replace lower values. JSON null is a present value. There is no delete syntax. The roots `mdoc` and `page` are reserved.

`--field` uses a non-empty RFC 6901 JSON Pointer, `=`, and one JSON value:

```sh
mdoc plan --field '/client/name="Example"'
mdoc plan --field '/approved=true' --field '/reviewers=["A","B"]'
mdoc plan --fields-file private/base.yaml --fields-file private/review.json
```

Use `{{ field "client.name" }}` for required author fields and `{{ optional "client.note" }}` for optional values. Use `{{ mdoc "generation" }}` for computed publish values. `{{ page "number" }}` and `{{ page "count" }}` are allowed only in headers and footers. They become Word PAGE and NUMPAGES fields. Templates cannot read files, environment variables, the network, or the clock.

Field values become document content. Do not use fields as a secret store. Normal config, validation, status, plan, and recovery output shows only counts and hashes.

### Table of contents

The generated static table of contents is opt-in for bundle publications. Omit it or set `enabled: false` to produce no TOC. Active TOC settings on a source publication fail validation instead of being ignored. `depth` selects heading levels 1 through 6. An omitted `title` uses `Table of Contents`. `mdoc` adds heading bookmarks only for entries shown in the TOC and headings targeted by actual internal links. Disabling the TOC does not add TOC heading bookmarks.

The global depth applies to every member unless a member selector overrides it. A selector can exclude a member, set a different depth, or include and exclude exact heading IDs from that source:

```yaml
layout:
  table_of_contents:
    enabled: true
    mode: static
    title: Contents
    depth: 1
    members:
      - source: docs/final-report.md
        depth: 2
      - source: docs/audit.md
        depth: 2
        exclude_headings: [detailed-findings, references]
      - source: docs/raw-evidence.md
        enabled: false
```

`include_headings` is an exact allowlist. `exclude_headings` removes exact IDs after the allowlist check. IDs are scoped to the selected source and use the Pandoc heading ID before bundle namespacing. An exact ID must match one retained heading. Duplicate sources, unknown bundle members, unknown or ambiguous heading IDs, invalid depths, and an ID listed in both include and exclude fail local validation.

### Reference autolinks

A version 3 bundle can turn plain reference codes into internal links without adding Markdown link syntax. Each rule names one bundle member whose headings define the codes. `heading_prefix` treats the full pattern match at the start of a heading as the definition key.

```yaml
publications:
  review:
    kind: bundle
    reference_links:
      - id: operations
        pattern: '\bOP[0-9]{2}\b'
        definition:
          source: docs/operations.md
          match: heading_prefix
        unresolved: error
    members:
      - source: docs/report.md
      - source: docs/operations.md
```

With this rule, plain `OP01` text links to a heading such as `## OP01 - Credential control`. Text like `OP01-OP08` keeps the same visible form and links each visible endpoint. Intermediate codes are not present and are not added.

Rules run in listed order. A link created by one rule is not processed by later rules. mdoc does not rewrite existing links, images, inline code, code blocks, or raw content. Duplicate definitions and rules with no definitions fail validation. `unresolved` may be `error`, `warning`, or `preserve`.

Reference autolinks target sections inside the same bundle. They do not create deep section links between separate Google Docs.

### Layout limits

- Lengths use `in`, `pt`, `cm`, or `mm`. Negative and unitless lengths are rejected.
- Named page sizes are `letter`, `legal`, and `a4`. Explicit width and height must both be set and must be from 1 through 22 inches.
- Each margin can be at most 10 inches. Opposing margins must leave at least 0.5 inch of usable page width and height.
- Page number starts are integers from 1 through 32767.
- Member starts are `continuous`, `after_cover`, `new_page`, `odd_page`, or `new_section`.
- Static table of contents global and member depths and member title heading levels are from 1 through 6 when enabled. A member depth of 0 inherits the global TOC depth.
- Wide table behavior is `warning`, `error`, `normal_flow`, or `landscape_section`. `wide_table_columns: 0` disables automatic wide table handling.
- Use `tables.preserve_width_styles` for template table styles that must keep a compact source width. `Cover Field Grid` and `CoverFieldGrid` are preserved by default.
- Generated bookmarks use Word safe ASCII names, are at most 40 characters, and are limited to selected TOC entries and internal link targets.

Headers and footers are optional and preserve the reference DOCX by default. Use `mode: inherit` to make that choice explicit. Use `mode: override` to replace only the configured default variant and optional `first_page` variant. Use `mode: remove` to suppress the selected header or footer. Existing configs that provide content without a mode continue to use override behavior.

```yaml
layout:
  header:
    mode: inherit
  footer:
    mode: override
    default:
      left: '{{ mdoc "publication.id" }}'
      right: 'Page {{ page "number" }} of {{ page "count" }}'
    first_page:
      center: Internal review
  tables:
    preserve_width_styles: [Metadata Grid]
```

Left, center, and right are optional inside an overridden variant. Use `default: {}` to replace a template default header or footer with an explicit blank variant. An omitted variant remains unchanged. The flat `left`, `center`, and `right` keys remain supported for old configs but cannot be combined with `default`. Generated headers and footers use section-aware paragraph tab stops, so they do not depend on table pagination. Body tables use fixed section-aware DXA widths. Nested tables use their containing cell width. A heading directly before a table overrides inherited keep-with-next behavior so long tables do not leave a heading alone on a full page.

## Sources and validation

Profile sources accept files, directories, and recursive glob patterns. Includes and excludes use stable normalized paths. Source paths must remain under the project root unless their root is listed in `external_roots`. Path traversal, casing conflicts, duplicate source keys, and symlink escapes are rejected.

Profiles control title rules, heading jumps, unpublished Markdown links, image types, table width, and document naming. A neutral profile can omit `reference_docx`. Naming can use the first H1, front matter, file name, or an explicit mapping.

Run local checks without Google access:

```sh
mdoc status
mdoc validate
mdoc doctor
```

These commands also support direct mode without a project:

```sh
mdoc status README.md
mdoc validate --root . README.md
mdoc doctor docs
```

Direct mode does not create project identity, publish state, auth clients, or Google clients. Setup, publish, plan, remote status, open, profile, and state commands require a project config.

## Styles and custom filters

Set `reference_docx` only when a project needs a custom Word style. The default uses Pandoc's neutral reference document.

Custom Lua filters are trusted executable input. A configured filter runs with your local user permissions during publish. Review filter code before adding it. Keep filter paths under the project root, global config directory, or an approved style root. Config, status, validation, plan, and doctor may inspect or hash filters, but they do not execute them.

For bundles, filters may change ordinary body text and styling. They may not add, remove, reorder, or change protected headings, heading IDs, member boundaries, local or publication links, image targets, or reserved layout markers. Publish runs filters once, checks the protected topology, and stops before DOCX conversion and Google writes if it changed.

## Google authorization

Create a Google Cloud desktop OAuth client. Enable the Drive and Docs APIs. Configure the consent screen and Workspace approval required by your organization.

Set credentials without putting values in shell history:

```sh
read -r "MDOC_GOOGLE_CLIENT_ID?Google client ID: "
export MDOC_GOOGLE_CLIENT_ID
read -rs "MDOC_GOOGLE_CLIENT_SECRET?Google client secret: "
printf '\n'
export MDOC_GOOGLE_CLIENT_SECRET
```

Do not commit these values. Deprecated `MEC_GOOGLE_CLIENT_ID` and `MEC_GOOGLE_SECRET` values still work with one warning. The warning prints variable names, never values.

Then sign in:

```sh
mdoc auth login
mdoc auth status
```

## Destination setup

The default destination uses separate staging and review folders. Setup can create them in My Drive, create them under a parent, or adopt explicit existing folders:

```sh
mdoc setup --profile default
mdoc setup --parent-folder-id PARENT_ID
mdoc setup --staging-folder-id STAGING_ID --review-folder-id REVIEW_ID
```

Use `--staging-folder-name` and `--review-folder-name` to set create-time display names. Existing folders are not renamed when these values change.

Setup reads the parent and both roles before its first write. It rejects inaccessible folders, duplicate metadata, role conflicts, folder collisions, missing capabilities, and Shared Drive folders. Safe adopted folders receive exact project, profile, and role metadata. Setup saves state only after both roles resolve. A retry reconciles a role completed before a partial failure. It never deletes or renames a user folder as rollback.

New state records the signed-in account. Remote commands reject another account. Legacy state without an account stays readable and is backfilled only by setup.

## Plan, publish, and status

Use separate commands for local status, planning, and remote status:

```sh
mdoc status --profile default
mdoc plan --profile default
mdoc publish --profile default
mdoc status --profile default --remote
mdoc open --profile default
```

`mdoc status` is local and can return `ok`, `partial`, or `invalid`. A missing optional capability such as Pandoc, a style file, or state is reported without contacting Google. `mdoc status --remote`, `mdoc plan`, and `mdoc doctor` are read only.

For version 3 publications, status also reports whether review pull is enabled, whether a sealed baseline is available, and whether pull is ready, changed, blocked, or unavailable. An active target without a baseline requires a new review generation. Reconcile can restore a snapshot reference only from a sealed local snapshot that matches the recovered target. It never uses the current Google document as a replacement baseline. See the document sync guide below for the full review flow.

For JSON consumers, version 1 and version 2 configs keep schema version 1. Version 3 validation, plan, publish, status, and open results use schema version 2. Target results use `target`, `publication_id`, and `publication_kind`, with `source` or `member_count` when applicable. Old consumers that treated status as a publish plan must call `mdoc plan`. `mdoc publish --dry-run` remains an alias with the same versioned plan payload.

For version 1 and version 2, `--file docs/report.md` selects its source. For version 3, `--file` selects every configured source publication that uses the source. Use `--bundle handbook` to select one bundle for validate, plan, publish, status, remote status, doctor, or open. `--file` and `--bundle` cannot be combined for planning or publishing. Use `--new-review` to create another immutable generation even when content is unchanged.

```sh
mdoc validate --bundle handbook
mdoc plan --bundle handbook
mdoc publish --bundle handbook
mdoc status --bundle handbook --remote
mdoc open --bundle handbook
```

## Document sync and review pull

Document sync is an opt-in review workflow for version 3 source and bundle publications. It is not a live or automatic two-way sync. Markdown stays canonical. `mdoc review pull` compares the active Google Doc and the current Markdown with a sealed publish baseline, then returns a patch for safe text edits. It never applies the patch itself.

For a complete start-to-finish guide, including uploaded DOCX review copies and manual recovery, see [Google Docs Review Workflow](GOOGLE_DOCS_REVIEW_WORKFLOW.md).

### Enable sync

Enable review pull on each publication that should accept Google Docs edits:

```yaml
version: 3
profiles:
  review:
    entry: handbook
    publications:
      handbook:
        kind: bundle
        review:
          pull:
            enabled: true
        members:
          - source: docs/overview.md
          - source: docs/operations.md
```

The setting only affects new review generations. After enabling it, publish with `--new-review`. The publish stores a private, sealed snapshot next to the configured state file. The snapshot contains source text. Do not share or commit its directory.

### Sync commands

| Command | Purpose |
|---|---|
| `mdoc publish --bundle handbook --new-review` | Publish a fresh review generation and capture its sync baseline. |
| `mdoc status --bundle handbook` | Check the local baseline and review pull state without Google access. |
| `mdoc status --bundle handbook --remote` | Check the active Google Doc, open suggestions, and whether remote content changed. |
| `mdoc open --bundle handbook` | Open the active Google Doc for review. |
| `mdoc review pull handbook` | Read review edits and print a proposed patch. The publication argument defaults to the profile entry. |
| `mdoc review pull handbook --output review.patch` | Also write the patch to a file with mode `0600`. Existing files are not replaced. |
| `mdoc review pull handbook --output review.patch --overwrite` | Replace an existing regular patch file. The output cannot be a symlink or a publication source. |
| `mdoc review pull handbook --json` | Return the review result, change counts, conflicts, and patch as JSON. |
| `mdoc review bootstrap handbook --document DOC_URL` | Compare an explicitly selected external Google Doc with the active sealed baseline. |

`review pull` also accepts `--config`, `--profile`, `--state`, and `--quiet`. It selects a publication by its positional ID, not with `--bundle`.

### Bootstrap an uploaded review copy

Use bootstrap when a DOCX generated from the publication was uploaded into a different Google Doc and that copy was edited. The selected publication must still have a valid sealed review snapshot. Bootstrap reuses that baseline and source map but reads the review content from the explicit document:

```sh
mdoc review bootstrap handbook \
  --document https://docs.google.com/document/d/DOCUMENT_ID/edit \
  --output review.patch
```

`--document` accepts a canonical Google Docs URL or a plain document ID. The command requires access to that file through the configured Google account. It verifies a stable native Google Doc, the captured tab topology, and resolved suggestions. It does not add metadata, adopt the external document, change state, or modify either Google Doc. Human and JSON output identify both the sealed baseline document and the selected external document.

If the document was created or uploaded outside mdoc, the `drive.file` permission may not let mdoc read it. Download the edited Google Doc with **File > Download > Markdown (.md)**, then use that exact export:

```sh
mdoc review bootstrap handbook \
  --document https://docs.google.com/document/d/DOCUMENT_ID/edit \
  --review-export edited-review.md \
  --output review.patch
```

The local export path still validates the document ID, sealed baseline, source map, source snapshots, and current Markdown. It cannot verify the live Google revision, tab topology, suggestions, or comments. The report marks those remote checks as not verified. The command rejects symlinks, non-regular files, empty exports, invalid UTF-8, and exports over 10 MiB.

Google import differences or broad edits may leave both safe and blocked changes. Add `--partial` with `--output` to write only the proven safe subset. The command still returns a conflict or unsupported result and lists every blocked region. Resolve those regions separately before treating the sync as complete.

Bootstrap accepts the same `--config`, `--profile`, `--state`, `--json`, `--quiet`, `--output`, and `--overwrite` options as pull. The resulting patch has the same safety limits and must be reviewed before application.

### Author workflow

Run the commands from the project root so the patch paths match the Markdown sources.

1. Set up the profile, validate it, and publish the first sync baseline.

   ```sh
   mdoc setup --profile review
   mdoc validate --profile review --bundle handbook
   mdoc publish --profile review --bundle handbook --new-review
   ```

2. Open the active document and make review edits in Google Docs.

   ```sh
   mdoc open --profile review --bundle handbook
   ```

   Accept or reject every open suggestion before pulling. Comments may remain open. Pull reports their count but does not print their text.

3. Check that the target and baseline are ready, then create a patch.

   ```sh
   mdoc status --profile review --bundle handbook --remote
   mdoc review pull handbook --profile review --output review.patch
   ```

4. Inspect and apply the patch. In a Git worktree:

   ```sh
   git apply --check review.patch
   git apply review.patch
   ```

   Outside Git, inspect the patch first and use `patch -p1 < review.patch`.

5. Validate the changed Markdown and publish a new review generation.

   ```sh
   mdoc validate --profile review --bundle handbook
   mdoc publish --profile review --bundle handbook --new-review
   ```

The new generation becomes the next review baseline. Repeat steps 2 through 5 for later review rounds.

### Pull results and limits

| Result | Meaning | Next action |
|---|---|---|
| `review_no_changes` | The Google Doc has no supported source changes. | No patch is needed. |
| `review_clean_patch` | Supported edits can be applied cleanly to the current Markdown. | Review and apply the patch. |
| `review_conflict` | A remote edit conflicts with local Markdown or document structure. | Resolve the reported locations, then pull again. |
| `review_unsupported` | The Google Doc contains an edit mdoc cannot map safely. | Make that edit in Markdown and publish a new generation. |

Pull supports plain text edits in paragraphs, headings, list items, and block quotes. Heading level, list kind, inline style, author link or image target, table, code, image, drawing, SVG, layout, member move, and tab structure edits are unsupported or conflicts. Pull also stops for open suggestions, an unstable export, a missing or damaged baseline, or a remote target identity mismatch.

Pull is read only. It does not change Markdown, Google Docs content, comments, suggestions, metadata, state, or snapshots. It creates a patch file only when `--output` is present and a clean patch exists. Review snapshots use source map schema 2. A schema 1 baseline remains as recovery history but cannot be used for pull. Publish a new review generation to capture a current baseline.

The first delivery retains all review snapshots. There is no cleanup command. `mdoc status` lists confirmed orphan snapshot paths. Remove an orphan manually only after checking that no active or prior target needs it.

## Recovery

An interrupted publish resumes from its local operation journal before it creates a new plan. The journal freezes target identity, members, resolved fields, computed values, layout, assets, filters, links, rendered files, generations, operation identity, review set, and folder IDs. The printed recovery command does not need the original field flags. If another frozen input changed, restore it before retrying.

A partial failure prints the safe resume command and recovery identity. JSON errors include the same data in `recovery`.

Recover missing or damaged local mapping after a safe remote scan:

```sh
mdoc state reconcile --profile default
```

Clear an unusable publish journal only after the remote safety scan:

```sh
mdoc state reconcile --profile default --abandon-operation
```

Record an intentional legacy source rename:

```sh
mdoc state remap --profile default --from docs/old.md --to docs/new.md
```

Version 3 publication targets do not use state remap. Edit the configured source while keeping the publication ID.

State version 5 adds sealed review snapshot references to the version 4 target identities. The first save after loading state older than version 4 writes one protected `<state-file>.pre-v4.bak` file with mode `0600`. It never overwrites that backup. Migration preserves active and prior source target history as `source:<source-key>`.

To roll back, first stop all mdoc commands and confirm that no publish or remap journal is active. Restore the `.pre-v4.bak` file only with the older mdoc binary that owns that state format. Do not restore it during an unfinished operation. Normal reconciliation and remap preserve account binding, locks, atomic state writes, and durable recovery journals.

## DOCX and Google import limits

The normal CLI does not expose a local DOCX export or inspect command. Release acceptance writes a test-only candidate DOCX, renders every page, and imports those exact bytes. That artifact is not part of the packaged operator interface.

Google Docs can change pagination, section behavior, headers, footers, page fields, table widths, bookmarks, and image placement during DOCX import. The static table of contents uses links but no page numbers because import can repaginate it. A layout control is supported only after the release acceptance log records that it survives import. Unsupported controls must be rejected, downgraded by an explicit policy, or left deferred.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 2 | command or config error |
| 3 | validation failed |
| 4 | authentication or permission failed |
| 5 | review or remote conflict |
| 6 | conversion failed |
| 7 | Google API failed |
| 8 | partial publish or recovery required |

## Build and package

```sh
go test ./...
go test -race ./...
go vet ./...
./scripts/package-release.sh
```

The package script derives the module path through `go list -m` for version injection. It creates the macOS Apple silicon archive under `dist/`.
