package backplane

import (
	"fmt"
	"io"
	"os"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/gopherex/xlog"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
)

// ManifestCommand is the first argument that turns a service's binary into
// a manifest printer: "<binary> manifest" loads the configuration as Run
// would (file and environment; Consul is not reached), runs the service's
// declarations, prints the manifest as JSON on stdout and exits without
// starting anything. Its logs go to stderr.
const ManifestCommand = "manifest"

// manifestMode reports whether args ask for the manifest; extra arguments
// are a usage error.
func manifestMode(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}

	command := args[1:]
	if len(command) == 0 || command[0] != ManifestCommand {
		return false, nil
	}

	if len(command) > 1 {
		return true, fmt.Errorf("%w: usage: %s manifest", ErrConfig, args[0])
	}

	return true, nil
}

// printManifest writes m as indented JSON with a trailing newline.
func printManifest(w io.Writer, m *backplanev1.Manifest) error {
	raw, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(m)
	if err != nil {
		return fmt.Errorf("backplane: manifest: %w", err)
	}

	if _, err := fmt.Fprintf(w, "%s\n", raw); err != nil {
		return fmt.Errorf("backplane: manifest: %w", err)
	}

	return nil
}

// stderrLogger is the logger of a manifest run: stdout is the manifest's.
func stderrLogger() *xlog.Logger { return xlog.NewJSON(xlog.WithWriter(os.Stderr)) }
