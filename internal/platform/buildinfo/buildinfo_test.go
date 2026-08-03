package buildinfo

import "testing"

func TestParseRequiresSemanticVersionAndCommit(t *testing.T) {
	for _, fixture := range []struct{ version, commit string }{
		{"", "0123456789abcdef"},
		{"v1.0.0", "0123456789abcdef"},
		{"1.0", "0123456789abcdef"},
		{"1.0.0", "not-a-commit"},
		{"1.0.0", "ABCDEF0"},
	} {
		if _, err := Parse(fixture.version, fixture.commit, "2026-08-02T00:00:00Z", "27", "28"); err == nil {
			t.Fatalf("Parse(%q,%q) accepted invalid metadata", fixture.version, fixture.commit)
		}
	}
	if info, err := Parse("1.0.0-rc.1", "0123456789abcdef", "2026-08-02T00:00:00Z", "27", "28"); err != nil || info.Version != "1.0.0-rc.1" {
		t.Fatalf("valid Parse() info=%#v err=%v", info, err)
	}
}

func TestParseRejectsInvalidSchemaInterval(t *testing.T) {
	for _, bounds := range [][2]string{{"-1", "28"}, {"29", "28"}, {"x", "28"}, {"27", "x"}} {
		if _, err := Parse("1.0.0", "0123456", "2026-08-02T00:00:00Z", bounds[0], bounds[1]); err == nil {
			t.Fatalf("Parse() accepted schema bounds %q", bounds)
		}
	}
	if _, err := Parse("1.0.0", "0123456", "not-a-time", "27", "28"); err == nil {
		t.Fatal("Parse() accepted invalid build time")
	}
}
