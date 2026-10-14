package api

import (
	"io/fs"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/apifiles"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/routes"
)

// OpenAPIFS publishes a directory of OpenAPI JSON/YAML files with a root document.
// It accepts embed.FS or any fs.FS; use fs.Sub to select the API directory.
// Relative $ref links stay inside that directory. Files are snapshotted once;
// only the snapshot's hash and entry path enter the manifest. No bundler is needed.
// Declaration failures are returned by Service.Run, like other route errors.
func OpenAPIFS(files fs.FS, entry string) OpenAPIOption {
	return schemaFS(files, entry, backplanev1.SchemaFormat_SCHEMA_FORMAT_OPENAPI)
}

// GraphQLFSOption attaches a separately delivered introspection document.
type GraphQLFSOption interface {
	HTTPOption
	DeclOption
}

// GraphQLFS publishes GraphQL introspection JSON outside the manifest. Pass nil
// as Service.GraphQL's inline introspection when using this option.
func GraphQLFS(files fs.FS, entry string) GraphQLFSOption {
	return schemaFS(files, entry, backplanev1.SchemaFormat_SCHEMA_FORMAT_GRAPHQL)
}

type apiFiles struct{ files *routes.SchemaFiles }

func schemaFS(source fs.FS, entry string, format backplanev1.SchemaFormat) apiFiles {
	files, err := apifiles.Snapshot(source, entry)

	ref := &backplanev1.APISchemaBundle{Entry: entry, Format: format}
	if files != nil {
		ref.Hash = files.Hash()
	}

	return apiFiles{files: &routes.SchemaFiles{Files: files, Ref: ref, Err: err}}
}

func (a apiFiles) applyHTTP(m *routes.Managed)      { m.OpenAPI, m.SchemaFiles = nil, a.files }
func (a apiFiles) applyHTTPDecl(r *backplanev1.API) { a.applyDecl(r) }
func (a apiFiles) applyDecl(r *backplanev1.API) {
	r.Schema = &backplanev1.API_Bundle{Bundle: a.files.Ref}
}
func (a apiFiles) schemaFiles() *routes.SchemaFiles { return a.files }

func declaredFiles[T any](r *backplanev1.API, opts []T) *routes.SchemaFiles {
	for _, opt := range opts {
		if source, ok := any(opt).(interface{ schemaFiles() *routes.SchemaFiles }); ok {
			if files := source.schemaFiles(); files.Ref == r.GetBundle() {
				return files
			}
		}
	}

	return nil
}

func filesError(files *routes.SchemaFiles) error {
	if files == nil {
		return nil
	}

	return files.Err
}
