# TODO

Known gaps and deliberate simplifications from the UI redesign, kept here so
they don't get lost rather than because anything is broken.

## UI

- **GraphQL Query/Variables split.** The protocol switcher treats GraphQL as
  a single raw JSON body (`{ query, variables }` together), matching what
  `execute.go`'s `buildBody` actually sends — there's no backend support for
  editing query and variables as separate fields, so the UI doesn't pretend
  otherwise. Would need a `Body.GraphQLVariables`-shaped field on the model
  first.
- **Sidebar tree filter doesn't expand collapsed folders.** It only hides/
  shows rows already rendered — a match inside a collapsed folder stays
  invisible. `⌘K` (command palette) is the tool for "find this regardless of
  where it's collapsed"; the sidebar filter is deliberately just a quick
  narrow-what's-visible tool. Worth reconsidering if that distinction proves
  confusing in practice.
- **No body-editor toolbar/Prettify button.** SOAP has "Format XML" already;
  raw/GraphQL JSON bodies don't have an equivalent "prettify in place"
  action. Response bodies get JSON syntax highlighting; request bodies
  (still a plain `<textarea>`) don't, since coloring editable text needs a
  contenteditable/overlay approach, not a `<pre>` swap.
- **No XML syntax highlighting**, only JSON — `formatXml`'s output is
  plain-colored text in the response pane.
- **Form-data file uploads** are marked but not sent (pre-existing, see the
  comment in `renderFormDataTable` / `buildBody` in `execute.go` — a file
  field round-trips its shape but sends empty).
- **No manual light/dark theme toggle** — follows `prefers-color-scheme`
  only.

## Backend

- **OData detection is a stored tag, not inferred.** `RequestSpec.Protocol`
  is only ever set by the OData importer today — a hand-built OData request
  won't get the OData chip/tag unless someone sets it via the API directly
  (there's no UI to hand-set it, since the protocol switcher's OData option
  writes it for you once selected).
- **Native collection export/import round-trips fresh IDs**, so re-importing
  an exported collection is always a full copy, never an update-in-place of
  the original. That's intentional (avoids ID collisions across machines)
  but means "export, tweak, re-import" doesn't merge — it duplicates.
