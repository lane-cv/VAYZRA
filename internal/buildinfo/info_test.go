package buildinfo

import "testing"

func TestDefaults(t *testing.T) {
	if Name() != "HappyLearn" {
		t.Fatalf("Name() = %q", Name())
	}
	if Version() == "" {
		t.Fatal("Version() must not be empty")
	}
}
