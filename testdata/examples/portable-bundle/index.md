# Example project

## Bundle inventory

| ID | Document | Section | Path | Role |
| --- | --- | --- | --- | --- |

Fill this table with app-authorized Google file links before planning. An empty
inventory intentionally cannot export. Example row conventions:

- A document row has a stable ID, one ordinary hyperlink, role `document`, and
  either a configured Section or an exact Path such as `docs/overview.md`.
- An `asset-tree` row links a Drive folder and specifies an exact destination such
  as `docs/architecture/diagrams`. Its source README and filenames stay intact;
  generated navigation appears separately at `navigation/<id>.md`.
- An `asset` row selects one supplemental file and requires an exact Path.
- A `reference` row stays online and leaves Section and Path empty.

Use the same canonical links in the online index and local index copy. Pasted
images in native Docs need no separate asset upload. Markdown images can link a
Drive image directly or resolve relative names within a declared asset tree.
