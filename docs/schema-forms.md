# Schema forms

`@backplane-labs/backplane-schema-forms` consumes `@gopherex/schemapb@1.9.7` from
GitHub Packages. It uses the existing engine for descriptor validation,
defaults, normalization, CEL computation, conditional visibility, constraints
and masking. It does not implement a second validation engine.

`SchemaForm` accepts a protobuf `Schema`, native `initialValues`, an accessible
`label` and `onSubmit(baked, signal)`. Values retain `bigint`, bytes and protobuf
timestamps/durations. Use `resetKey` when loading a different record/revision.
The schema must be fully linked before rendering. Invalid descriptors produce
a schema error state. Only English resources are supplied; the exported
`schemaFormsEnglish` namespace is `backplane.schemaForms`.

The renderer supports scalar kinds, Choice and dynamic options, Object, Ref,
OneOf, List and Map. JSON and deep structures use a typed protobuf JSON editor,
which preserves integer precision. Missing and null values remain distinct.
Computed/immutable fields cannot be edited. Inactive values remain in the draft
and reappear when their CEL condition becomes true. Structured validation paths
match literal keys, including dots and brackets. Schema-authored descriptions,
titles, units and deprecation markers are rendered as text.

Submit produces the engine's canonical Baked snapshot. Blocking validation or
malformed JSON prevents submission. A failed submit preserves the draft and
does not retry the mutation. Duplicate submits are suppressed while pending;
unmount aborts the callback signal. Reset restores the baseline; a successful
submit establishes a new baseline. `onDirtyChange` supports a host navigation
guard; browser unload also warns while a draft is dirty.

`SchemaValuesPreview` is a separate read-only masked projection. Never use its
output as `initialValues`: a display mask is not an editable secret. Secret
inputs conceal text, require explicit reveal for structured values, and are
masked in read-only mode. The caller remains responsible for access to the
original editable configuration and for handling server-side validation.

Catalog: `Kit/Schema forms` includes editable and read-only fixtures. Browser
checks cover both themes, accessibility, exact uint64, dynamic controls,
variant switching, save failure recovery and masked previews. Model tests
cover defaults/CEL, nanosecond timestamps, literal path segments and masking.
