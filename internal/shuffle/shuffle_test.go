package shuffle_test

import (
	"slices"
	"strconv"
	"testing"

	"github.com/backplane-labs/backplane/internal/shuffle"
)

func TestStrings(t *testing.T) {
	t.Parallel()

	for _, s := range [][]string{
		nil,
		{},
		{"only"},
		{"a", "b"},
		{"a", "b", "c", "d", "e", "f", "g", "h"},
	} {
		before := slices.Clone(s)

		shuffle.Strings(s)

		if !slices.Equal(slices.Sorted(slices.Values(s)), slices.Sorted(slices.Values(before))) {
			t.Errorf("Strings(%q) = %q: not a permutation", before, s)
		}
	}
}

func TestStringsMoves(t *testing.T) {
	t.Parallel()

	s := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	before := slices.Clone(s)

	shuffle.Strings(s)

	if slices.Equal(s, before) {
		t.Errorf("Strings kept the order of %q", s)
	}
}

func BenchmarkStrings(b *testing.B) {
	s := make([]string, 16)
	for i := range s {
		s[i] = strconv.Itoa(i)
	}

	for b.Loop() {
		shuffle.Strings(s)
	}
}
