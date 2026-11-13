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

## Google-authored working drafts

`mdoc working-draft` reads an existing Google Doc without publishing Markdown or creating a review baseline. It emits JSON and never edits the remote document. Use `mdoc auth login` and grant the existing document to the same OAuth app/account through Google Picker first; `drive.file` login alone does not authorize an arbitrary document URL.

```sh
mdoc working-draft inspect DOCUMENT_ID
mdoc working-draft capture DOCUMENT_ID > baseline.json
# After a collaborator edits the Google Doc:
mdoc working-draft capture DOCUMENT_ID > incoming.json
mdoc working-draft compare baseline.json incoming.json current.json
mdoc working-draft discussion DOCUMENT_ID --all
```

`current.json` contains the caller's current normalized `{"title":"...","body":"..."}`. For an unchanged local baseline, extract `normalized_content` from `baseline.json`. Comparison is offline, returns incoming content and explicit local divergence, and does not apply it. Save redirected snapshots in a private directory; redirection permissions belong to your shell.

Check `body_usable` and `diagnostics` before using a capture. Initial support is one root tab in a native My Drive Doc: paragraphs, headings, ordinary bullet/decimal lists, bold/italic/strikethrough and external links. Suggestions, tables, images, chips, footnotes, internal links and unverified structures block body use. Discussion remains readable. Paginated/failed refreshes never authorize deletion of unseen records; merge IDs and honor explicit deletion flags. `--all` retains partial results on stdout and exits with a structured error if interrupted.

Applications import `github.com/goliatone/mdoc/workingdraft`, call `New(Options{HTTPClient: authenticatedClient})`, and use the same typed operations. Token sources, providers, converters and byte-oriented snapshot stores are injectable. `Compare`, `DecodeSnapshot`, `VerifySnapshot`, `SaveSnapshot` and `LoadSnapshot` are usable without global configuration. `NewAuth` provides noninteractive Start/Finish/Status/TokenSource helpers with an explicit credential store and trusted callback URL. Bind opaque account references to the host actor/project; no library operation creates hidden storage or opens a browser.

The built-in conversion version is `google-docs-prose-v2`. Recapture both baseline and incoming snapshots when moving from v1; comparison rejects mixed conversion versions. Snapshot evidence removes temporary image `contentUri` access URLs before hashing or storage. Older snapshots containing those fields are rejected and must be recaptured.

Custom `Provider.Document` implementations now return the original `json.RawMessage`, preserving absent versus explicitly false style properties. Do not round-trip responses through SDK structs. Converters receive resolved effective text styles, while snapshot evidence preserves original field presence. Named-style inheritance, independent list boundaries and parent marker widths are preserved; ambiguous nesting and code-like mixed indentation block body use.

Custom authentication stores now implement `Load(ctx, accountRef) (CredentialRecord, error)` and `CompareAndSwap(ctx, accountRef, expectedRevision, nextRecord) (bool, error)`. Persist the token, unique revision and connection ID together in private storage. CAS must be atomic across every process using the store; return false without mutation on a revision mismatch. Load returns an empty record for an absent account, and revisions must never be reused. Reconnection changes the connection ID and invalidates previous token sources and pending callbacks. This replaces the earlier unconditional Load/Save API; a process-local mutex around Save is insufficient for shared storage.

## Explicit document contributions

The additive `contribution` package and `mdoc contribution` commands create a supported document, change one interior text span, or reply to an existing unresolved comment. They use the existing OAuth account and app-file access. They do not publish review generations, change sharing, resolve comments, accept reviews, or synchronize content automatically.

Preparation reads and renders only. Execution requires the exact retained plan, its digest, the same private journal and `--write`. To try the current checkout, build a local binary with `go build -o /tmp/mdoc-contribution ./cmd/mdoc`.

```sh
umask 077
mkdir -p .mdoc-contributions
cat > .mdoc-contributions/create-input.json <<'JSON'
{
  "meta": {"namespace": "local-review"},
  "parent_id": "APP_AUTHORIZED_MY_DRIVE_FOLDER_ID",
  "title": "Design review",
  "markdown": "# Design review\n\nA **prepared** document."
}
JSON
/tmp/mdoc-contribution contribution prepare-create \
  --input .mdoc-contributions/create-input.json \
  --journal .mdoc-contributions/journal > .mdoc-contributions/create-plan.json
# Review input.markdown, input.title, input.parent_id and the retained artifact.
/tmp/mdoc-contribution contribution create --plan .mdoc-contributions/create-plan.json \
  --expect-digest "$(jq -r .digest .mdoc-contributions/create-plan.json)" \
  --journal .mdoc-contributions/journal --write
/tmp/mdoc-contribution contribution inspect --plan .mdoc-contributions/create-plan.json \
  --journal .mdoc-contributions/journal
```

Preparation supplies schema version 1, a random operation ID and the current opaque login binding when omitted. Supply a retained `meta.operation_id` to repeat preparation of the same logical operation; omitting it prepares a new operation. Reusing an ID for different content conflicts. Never use a second journal to retry the same operation. Journals contain private plans/receipts, use directory mode `0700` and file mode `0600`, and must be kept with their plans. Journal opening requires directory-sync support throughout its ancestry and fails before dispatch if durability cannot be established.

For replies, use `prepare-reply --input reply-input.json --journal ...` then `reply --plan reply-plan.json --expect-digest ... --journal ... --write`. Input:

```json
{
  "meta": {"namespace": "local-review"},
  "source": {"document_id": "DOCUMENT_ID"},
  "comment_id": "EXISTING_UNRESOLVED_COMMENT_ID",
  "content": "Reviewed; the proposed wording looks correct."
}
```

Use `working-draft discussion DOCUMENT_ID --all` to find comment IDs. Replies work independently of body conversion. The immediate thread recheck is not atomic with Google's reply creation, so a concurrent thread change remains possible.

For updates, capture with `working-draft capture DOCUMENT_ID`, check `body_usable`, and use the native paragraph/run indices in `raw_content`. `prepare-update` accepts `meta`, `source`, `expected_revision`, `selection` and `replacement`; `selection` contains `paragraph_start`, rune offsets `start`/`end`, exact `expected_text` and `paragraph_digest`. Compute the latter with `contribution.ParagraphDigest(nativeParagraphWithoutFinalNewline)`. The plan previews `before`/`after` and binds the resolved tab, UTF-16 range and native styling. Execute with `apply-update --plan ... --expect-digest ... --journal ... --write`. A revision conflict requires fresh preparation and a new operation ID.

Only a span strictly inside one original unlinked text run is eligible: in `abcd`, replacing `bc`, deleting `bc`, or inserting between `b` and `c` works. Whole-run changes, run/paragraph edges, links, empty paragraphs, suggestions and unsupported structures fail before mutation. Replacement preserves exact Unicode text, including astral characters; it rejects newlines, controls except tab, and Google-stripped U+E000–U+F8FF characters. No formatting requests are emitted.

Create supports headings, paragraphs, lists, emphasis and explicit HTTP(S) links. It requires Pandoc and an existing writable My Drive folder. Images, tables, raw HTML, local links, filters and external resources are rejected. Limits are 1 MiB Markdown, 10 MiB DOCX, 1 KiB title, 64 KiB replacement/reply, 10 MiB per response and 100 discovery pages. Shared Drives and multiple tabs remain unsupported.

Execution emits an outcome on stdout even when it returns an error: `committed`, `not_sent`, `conflict`, or `unknown`. A repeated committed command returns its receipt without another provider call. Every claimed operation is single-send, including `not_sent`/`conflict`; correct the input and prepare a new operation when safe. Lost responses, process crashes in `sending`, and missing acknowledgment IDs require `inspect` or manual reconciliation. Inspection never resends, proves absence after a timeout, or attributes a matching reply solely by its text. Historical committed receipts survive later edits, deletion or lost access. Do not delete or reset a journal to escape uncertainty. Exit code 8 indicates reconciliation is required, including when a receipt cannot be saved after a write or inspection. Preserve stdout and the journal, repair storage, then inspect without resending; an acknowledged write remains `committed` in stdout even if its receipt save fails. If result output fails (including a closed pipe), unknown or acknowledged effects also return exit 8; inspect the retained plan with the same journal after restoring output. Provider and receipt-save errors remain available alongside the output error. Structured error codes distinguish other failures.

Create inspection reads a retained document ID directly. Its optional `creation` evidence reports trash status and whether metadata, destination and topology still match; missing access is reported as inaccessible. Historical commitment is preserved even when metadata discovery would miss the document. For uncertain operations, direct identity observation alone is not proof of commitment: recovery still requires complete, unique exact metadata discovery.

Applications import `github.com/goliatone/mdoc/contribution` and construct `New(Options{AccountBinding: connectionID, TokenSource: tokenSource})`, or supply an explicitly authenticated `HTTPClient`. Supply exactly one auth configuration; injected transports must not retry writes. The typed `PrepareCreate/Create`, `PrepareUpdate/ApplyUpdate`, `PrepareReply/Reply` and `InspectOutcome` APIs contain no ctx policies or SDK request DTOs. Use `DecodeCreatePlan`, `DecodeUpdatePlan` and `DecodeReplyPlan` for retained JSON. The service has no durable store: the host must authorize exact content, atomically bind operation/digest, serialize claims, persist `sending` before dispatch, and retain the outcome even when an error is returned. Set `Attempt.FirstTransmission` only for that fresh durable claim. A digest and attempt ID are not authorization. Reconnection requires a newly prepared plan; the CLI's persisted login generation survives refresh but changes on login/logout. Legacy credential files migrate under the credential lock to a random persisted generation on first use. Logout syncs token removal before reporting success; if persistence fails, repair storage and retry logout even if the token file is already absent. Plans prepared against an older derived legacy binding require new preparation; preserve any previously claimed operation for recovery rather than resending it.

Local SDK/CLI smoke covers create/replay, stale/fresh updates, replies, inspection, cross-process locks and crash/lost-response recovery. A maintainer-authorized disposable Google folder/document is still required to verify visible conversion and surrounding formatting before production adoption. No live write, acceptance, tag, release or ctx consumer adoption is implied by these checks.

## Portable document bundles

`mdoc bundle` exports an explicitly inventoried Drive collection to ordinary
Markdown and local image files. It reads Google content without changing it.
The existing `publish --bundle` command still publishes a local collection into
one Google Doc; its configuration and behavior are unchanged.

```sh
mdoc bundle init --directory ./bundle-project --project my-project
# Fill bundle-project/index.md with canonical Google links, or configure a native Docs index.
mdoc bundle plan --config ./bundle-project/mdoc-bundle.yaml --json
mdoc bundle export --config ./bundle-project/mdoc-bundle.yaml --output ./snapshot
mdoc bundle verify ./snapshot --json
```

Plan/export use the existing explicitly selected account from `mdoc auth login`.
Every required file must be accessible to that app/account; index or folder access
is not an access grant to all descendants. Init and verify never select credentials.
Pandoc must be installed for conversion and verification. Readers need only a
Markdown viewer. Move the entire output directory to preserve navigation/images.
The destination parent must exist; export never overwrites an existing path.
Exclusive directory installation currently supports macOS and Linux.

The single authored inventory is a table under `Bundle inventory` with exactly
`ID`, `Document`, `Section`, `Path`, and `Role` columns. Each Document cell contains
one ordinary canonical Google hyperlink. IDs and source file identities must be
unique. Roles are `document`, `asset`, `asset-tree`, and `reference`. Documents
use configured section directories and filename templates, or an exact Path.
Assets/trees require a Path; references have no local destination. Prose links do
not add membership. A native Docs table and a stored/local Markdown index use the
same convention. [The starter example](testdata/examples/portable-bundle/mdoc-bundle.yaml)
shows nesting, optional ID prefixes, and preserved entry-point names.

Default names are lowercase kebab-case; exact paths retain spelling. File identity
is independent of its name. Configure `layout.index`, `layout.sections`,
`layout.assets`, `layout.asset_index`, and `naming` in the separate version-1
`mdoc-bundle.yaml`. Unknown fields/tokens, traversal, file/directory conflicts and
Unicode/case-insensitive collisions fail explicitly. Generated asset-tree listings
live outside copied trees, preserving source `README.md` and `manifest.json` files.

Native Docs support one root tab, text/headings, lists, emphasis, ordinary links,
plain pasted images and simple rectangular tables. Stored Markdown uses GFM.
Embed images in native Docs; authors need not upload a separate copy. Markdown
images may use Drive image links or original relative paths within declared asset
trees. Relative paths resolve before destination layout changes. Native online
reading should use embedded images and canonical Google links; Drive preview is
not presumed to resolve arbitrary Markdown-relative resources.

PNG, JPEG, GIF and a conservative self-contained static SVG subset are validated
and copied. Missing figures, drawings/charts, cropped/transformed native images,
raw embeds, suggestions, complex tables, multiple tabs, Shared Drives and arbitrary
remote image hosts prevent installation. There is no diagram regeneration or
permission/scope escalation. Local index images use canonical Drive links.

`local_complete` means required files/images and local navigation passed checks.
`no_online_dependencies` separately reports whether any navigation remains online.
External/archive references and unverifiable canonical fragments stay online with
diagnostics; `links.unresolved_fragments: error` makes unverifiable fragments fail.
Verification checks actual files, hashes, links/anchors, image decoding and path
containment without network access. It does not prove source authenticity or approval.

The manifest records source versions/revisions, capture intervals, content/asset
hashes and occurrence provenance, with no temporary image capabilities. A changing
source or expired native image URL restarts the whole capture, at most three times.
Captures are stable per source, not an atomic snapshot of the entire collection.
Default limits are 100 documents, 1,000 asset sources/image occurrences, tree depth
8, 10 MiB/source, 20 MiB/image and 500 MiB cumulative/installed bytes. Explicit
ceilings are 1,000 documents, 10,000 assets/occurrences, depth 32, 50 MiB/source,
100 MiB/image, 2 GiB total and three attempts. Failed/canceled attempts do not install
output; transfer budgets include retries.

Focused local adapter/CLI checks establish trial readiness. A maintainer-owned
read-only live trial still needs to confirm access, visible image fidelity and
online navigation for the intended collection before acceptance or release.
