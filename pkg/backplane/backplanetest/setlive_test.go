package backplanetest_test

import (
	"errors"
	"testing"

	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

var errPoolRefused = errors.New("pool refused")

// SetLive runs the field's appliers: an error comes back and the field
// keeps its value, watchers quiet.
func TestSetLiveApplies(t *testing.T) {
	t.Parallel()

	size := config.LiveOf(4)

	var watched []int

	size.Apply(func(v int) error {
		if v > 8 {
			return errPoolRefused
		}

		return nil
	})
	size.Watch(func(v int) { watched = append(watched, v) })

	if err := backplanetest.SetLive(&size, 16); !errors.Is(err, errPoolRefused) || size.Get() != 4 || len(watched) != 0 {
		t.Fatalf("refused: %v, %d, %v", err, size.Get(), watched)
	}

	if err := backplanetest.SetLive(&size, 8); err != nil || size.Get() != 8 || len(watched) != 1 {
		t.Fatalf("applied: %v, %d, %v", err, size.Get(), watched)
	}
}
