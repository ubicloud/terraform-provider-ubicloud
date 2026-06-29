package planmodifiers

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// normalizeParentRef must reduce a canonical parent path to its name segment and pass
// a bare name/id through unchanged (after trimming), so a user-supplied name reference
// and the response's "/location/<loc>/postgres/<name>" path compare equal.
func TestNormalizeParentRef(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/location/aws-us-east-1/postgres/tf-acc-rrparent", "tf-acc-rrparent"},
		{"tf-acc-rrparent", "tf-acc-rrparent"},
		{"  tf-acc-rrparent  ", "tf-acc-rrparent"},
		{"  /location/aws-us-east-1/postgres/tf-acc-rrparent ", "tf-acc-rrparent"},
		{"/location/eu-central-h1/postgres/p1", "p1"},
		// A bare ubid has no path structure: it cannot be reduced to the parent's name,
		// because the response path never echoes the id. It passes through unchanged.
		{"pg4w8z0abcdefghijkmnpqrstuv", "pg4w8z0abcdefghijkmnpqrstuv"},
		{"", ""},
		// Not the postgres canonical shape: passed through (only trimmed).
		{"/location/aws-us-east-1/firewall/fw1", "/location/aws-us-east-1/firewall/fw1"},
	}
	for _, c := range cases {
		if got := normalizeParentRef(c.in); got != c.want {
			t.Errorf("normalizeParentRef(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ParentRefStability pins the planned parent back to prior state when both references
// resolve to the same parent (the import case: state holds the canonical path, config
// holds the name). It must leave the plan untouched for a genuine change, for create
// (null state), and for an unknown plan value.
func TestParentRefStability(t *testing.T) {
	const path = "/location/aws-us-east-1/postgres/tf-acc-rrparent"
	m := ParentRefStability()

	cases := []struct {
		name     string
		stateVal types.String
		planVal  types.String
		wantPlan types.String
	}{
		{
			name:     "import: name config vs path state -> pinned to state path",
			stateVal: types.StringValue(path),
			planVal:  types.StringValue("tf-acc-rrparent"),
			wantPlan: types.StringValue(path),
		},
		{
			// A configured parent is a valid Reference (name or id, never a slashed path),
			// so the plan side must NOT be path-reduced. Two different canonical paths that
			// share a leaf name (different locations) must not compare equal, or a genuine
			// parent change would be silently suppressed instead of replaced.
			name:     "collision: same leaf name, different location path -> not pinned",
			stateVal: types.StringValue(path),
			planVal:  types.StringValue("/location/eu-central-h1/postgres/tf-acc-rrparent"),
			wantPlan: types.StringValue("/location/eu-central-h1/postgres/tf-acc-rrparent"),
		},
		{
			name:     "created replica re-plan: name config vs name state -> unchanged",
			stateVal: types.StringValue("tf-acc-rrparent"),
			planVal:  types.StringValue("tf-acc-rrparent"),
			wantPlan: types.StringValue("tf-acc-rrparent"),
		},
		{
			name:     "genuine change: different parent name -> left for RequiresReplace",
			stateVal: types.StringValue(path),
			planVal:  types.StringValue("tf-acc-other"),
			wantPlan: types.StringValue("tf-acc-other"),
		},
		{
			// An id-shaped config value is ambiguous (an id always is id-shaped), so it may
			// reference a different parent by id; the modifier cannot disambiguate id from
			// name in a plan, so it does not pin and RequiresReplace decides (the documented
			// id-form residual: over-replaces, never suppresses a real change).
			name:     "id config vs path state -> not reconcilable, left unchanged",
			stateVal: types.StringValue(path),
			planVal:  types.StringValue("pgn30gjk1d1e2jj34v9x0dq4rp"),
			wantPlan: types.StringValue("pgn30gjk1d1e2jj34v9x0dq4rp"),
		},
		{
			// The dangerous collision: a parent NAMED like a ubid, reconfigured to a
			// different database's id with the same string. Because the config value is
			// id-shaped the modifier must NOT pin (else a genuine parent change is silently
			// suppressed); it errs toward replace instead.
			name:     "id-shaped name vs same-string path state -> not pinned",
			stateVal: types.StringValue("/location/aws-us-east-1/postgres/pgn30gjk1d1e2jj34v9x0dq4rp"),
			planVal:  types.StringValue("pgn30gjk1d1e2jj34v9x0dq4rp"),
			wantPlan: types.StringValue("pgn30gjk1d1e2jj34v9x0dq4rp"),
		},
		{
			// A name that merely starts with "pg" but is not a full ubid is unambiguously a
			// name, so it still pins (guard against over-excluding ordinary names).
			name:     "name starts with pg but not id-shaped -> pinned",
			stateVal: types.StringValue("/location/aws-us-east-1/postgres/pg-main"),
			planVal:  types.StringValue("pg-main"),
			wantPlan: types.StringValue("/location/aws-us-east-1/postgres/pg-main"),
		},
		{
			name:     "create (null state): plan untouched",
			stateVal: types.StringNull(),
			planVal:  types.StringValue("tf-acc-rrparent"),
			wantPlan: types.StringValue("tf-acc-rrparent"),
		},
		{
			name:     "unknown plan value: untouched",
			stateVal: types.StringValue(path),
			planVal:  types.StringUnknown(),
			wantPlan: types.StringUnknown(),
		},
		{
			name:     "whitespace-padded name config vs path state -> pinned",
			stateVal: types.StringValue(path),
			planVal:  types.StringValue("  tf-acc-rrparent  "),
			wantPlan: types.StringValue(path),
		},
	}

	for _, c := range cases {
		req := planmodifier.StringRequest{StateValue: c.stateVal, PlanValue: c.planVal}
		resp := &planmodifier.StringResponse{PlanValue: c.planVal}
		m.PlanModifyString(t.Context(), req, resp)
		if !resp.PlanValue.Equal(c.wantPlan) {
			t.Errorf("%s: PlanValue = %v, want %v", c.name, resp.PlanValue, c.wantPlan)
		}
		if resp.Diagnostics.HasError() {
			t.Errorf("%s: unexpected diags %+v", c.name, resp.Diagnostics)
		}
	}
}

// postgresIdRe must match exactly the backend's PostgresDatabase.id shape (openapi
// pattern ^pg[0-9a-hj-km-np-tv-z]{24}$) so an id-shaped configured parent is never pinned
// (it could reference a different database by id), while ordinary names still pin.
func TestPostgresIdRe(t *testing.T) {
	idShaped := []string{
		"pgn30gjk1d1e2jj34v9x0dq4rp",
		"pg000000000000000000000000",
	}
	for _, s := range idShaped {
		if !postgresIdRe.MatchString(s) {
			t.Errorf("postgresIdRe should match id %q", s)
		}
	}
	notIdShaped := []string{
		"tf-acc-rrparent",             // ordinary hyphenated name
		"pg-main",                     // starts with pg but hyphenated
		"pgn30gjk1d1e2jj34v9x0dq4r",   // one char short
		"pgn30gjk1d1e2jj34v9x0dq4rpp", // one char long
		"pgi30gjk1d1e2jj34v9x0dq4rp",  // contains excluded 'i'
		"pgo30gjk1d1e2jj34v9x0dq4rp",  // contains excluded 'o'
		"Pgn30gjk1d1e2jj34v9x0dq4rp",  // uppercase
		"vmn30gjk1d1e2jj34v9x0dq4rp",  // wrong resource prefix
		"",
	}
	for _, s := range notIdShaped {
		if postgresIdRe.MatchString(s) {
			t.Errorf("postgresIdRe should not match %q", s)
		}
	}
}
