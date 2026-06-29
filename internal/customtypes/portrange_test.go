package customtypes

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// TestCollapsePortRange covers the canonicalization that mirrors the backend's
// model/firewall_rule.rb display_port_range: an equal-bounds range "X..X" collapses to
// "X" (through the integer value, as the server does via to_i); a bare port, an unequal
// range, and an unrecognized string pass through unchanged.
func TestCollapsePortRange(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"22..22", "22"},
		{"53..53", "53"},
		{"0..0", "0"},
		{"00..00", "0"},          // server canonicalizes via to_i; "00..00" stores [0,1) -> "0"
		{"022", "22"},            // bare port with a leading zero canonicalizes like to_i
		{"0022..0023", "22..23"}, // both bounds canonicalize like to_i
		{"80..8080", "80..8080"},
		{"0..65535", "0..65535"},
		{"5432", "5432"},
		{"22", "22"},
		{"", ""},
		{"22..23", "22..23"},
		{"abc", "abc"},   // unrecognized: passed through for the server to reject
		{"22..", "22.."}, // not a full range: passed through
		{"..22", "..22"},
	}
	for _, c := range cases {
		if got := collapsePortRange(c.in); got != c.want {
			t.Errorf("collapsePortRange(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestPortRangeStringSemanticEquals covers the value-type semantic equality: forms that
// collapse to the same stored port are equal, forms that differ are not, and a non-
// PortRangeValue argument is an error.
func TestPortRangeStringSemanticEquals(t *testing.T) {
	ctx := t.Context()
	cases := []struct {
		a, b string
		want bool
	}{
		{"22..22", "22", true},
		{"22", "22..22", true},
		{"22..22", "22..22", true},
		{"22", "22", true},
		{"022", "22", true},            // leading-zero bare port equals canonical "22"
		{"0022..0023", "22..23", true}, // leading-zero range equals canonical "22..23"
		{"80..8080", "80", false},
		{"22..22", "23", false},
		{"0..65535", "0..65535", true},
	}
	for _, c := range cases {
		got, diags := NewPortRangeValue(c.a).StringSemanticEquals(ctx, NewPortRangeValue(c.b))
		if diags.HasError() {
			t.Errorf("StringSemanticEquals(%q, %q) returned diagnostics: %v", c.a, c.b, diags)
		}
		if got != c.want {
			t.Errorf("StringSemanticEquals(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}

	// A non-PortRangeValue argument is a hard error, not a silent false-equal.
	got, diags := NewPortRangeValue("22").StringSemanticEquals(ctx, basetypes.NewStringValue("22"))
	if !diags.HasError() {
		t.Error("StringSemanticEquals with a plain StringValue should return an error diagnostic")
	}
	if got {
		t.Error("StringSemanticEquals with a mismatched type should return false")
	}
}
