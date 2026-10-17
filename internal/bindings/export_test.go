package bindings

import "github.com/google/uuid"

// Wiring is what ReadWiring read: the file of every binding and rule.
type Wiring struct {
	Bindings map[string]string
	Rules    map[uuid.UUID]string
	Digest   string
}

// ReadWiring reads a wiring directory as Files does.
func ReadWiring(dir string) (Wiring, error) {
	d, err := readWiring(dir)
	if err != nil {
		return Wiring{}, err
	}

	out := Wiring{Bindings: map[string]string{}, Rules: map[uuid.UUID]string{}, Digest: d.digest}
	for hook, b := range d.bindings {
		out.Bindings[hook] = b.file
	}

	for id, r := range d.rules {
		out.Rules[id] = r.file
	}

	return out, nil
}
