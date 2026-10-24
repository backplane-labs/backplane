package wsproto

import (
	"slices"
	"testing"
)

func TestServices(t *testing.T) {
	t.Parallel()

	got := services([]string{"/b.v1.S/M", "/a.v1.S/X", "/b.v1.S/N", "bad"})
	if want := []string{"a.v1.S", "b.v1.S"}; !slices.Equal(got, want) {
		t.Fatalf("services = %v, want %v", got, want)
	}
}
