# TODO

Known gaps and deliberate simplifications from the UI redesign, kept here so
they don't get lost rather than because anything is broken.

## UI

- **Sidebar tree filter doesn't expand collapsed folders.** It only hides/
  shows rows already rendered — a match inside a collapsed folder stays
  invisible. `⌘K` (command palette) is the tool for "find this regardless of
  where it's collapsed"; the sidebar filter is deliberately just a quick
  narrow-what's-visible tool. Worth reconsidering if that distinction proves
  confusing in practice.

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
