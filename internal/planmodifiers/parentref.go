// Package planmodifiers holds this provider's custom terraform-plugin-framework plan
// modifiers, attached to generated schema attributes through the generator-config jq
// patch chain (config/plan_modifiers.jq) so they regenerate into the _gen.go schema
// rather than being hand-patched.
package planmodifiers

import (
	"context"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
)

// parentPathRe matches the postgres parent's canonical path as serialized by the
// backend (model/postgres/postgres_resource.rb path: "/location/<loc>/postgres/<name>").
// The capture group is the parent's name, the last segment. Names cannot contain a
// slash, so a single non-slash run captures the whole name.
var parentPathRe = regexp.MustCompile(`^/location/[^/]+/postgres/([^/]+)$`)

// postgresIdRe matches a postgres database id as serialized by the backend (the openapi
// PostgresDatabase.id pattern). An id is always id-shaped, so a reference that is NOT
// id-shaped is unambiguously a name. The modifier uses this to refuse to pin an id-shaped
// configured parent: such a value may reference a different database by id, and a plan
// modifier cannot resolve id vs name without a network lookup.
var postgresIdRe = regexp.MustCompile(`^pg[0-9a-hj-km-np-tv-z]{24}$`)

// normalizeParentRef reduces the backend's canonical parent path to the name that
// identifies the parent. The response echoes parent as "/location/<loc>/postgres/<name>",
// while the user configures it as a bare name; reducing the response path to its name
// segment lets the two compare equal. Only the state/response value is normalized: a
// configured parent is a valid Reference (name or id, no slashes), so it is compared
// raw (trimmed) by the caller. A bare name/id (and any non-canonical string) is returned
// trimmed but otherwise unchanged; a bare id cannot be reduced to the name because the
// path never carries the id, so the id form does not compare equal to the path (an
// accepted limitation).
func normalizeParentRef(s string) string {
	s = strings.TrimSpace(s)
	if m := parentPathRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return s
}

// parentRefStability keeps the prior state parent when the planned value refers to the
// same parent. An imported read replica holds the canonical path in state; the user
// configures parent as the name. Without this, parent's RequiresReplace modifier would
// compare the path and the name as different strings and force a spurious replace. By
// pinning the planned value to prior state when both resolve to the same parent, the
// later RequiresReplace sees equal values and does not replace; a genuine parent change
// still differs and replaces.
type parentRefStability struct{}

// ParentRefStability returns the parent reference stability plan modifier.
func ParentRefStability() planmodifier.String {
	return parentRefStability{}
}

func (m parentRefStability) Description(_ context.Context) string {
	return "Keeps the prior parent reference when the planned value points at the same parent (name or id vs canonical path), so an imported read replica does not churn."
}

func (m parentRefStability) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m parentRefStability) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	// No prior state (create) or an unresolved value: nothing to reconcile against.
	if req.StateValue.IsNull() || req.StateValue.IsUnknown() {
		return
	}
	if req.PlanValue.IsNull() || req.PlanValue.IsUnknown() {
		return
	}
	plan := strings.TrimSpace(req.PlanValue.ValueString())
	// An id-shaped reference is ambiguous: it may identify a different database by id than
	// the name in the state path's leaf, and a plan modifier cannot disambiguate id from
	// name without a network lookup. Do not pin it; let RequiresReplace decide (the
	// documented id-form residual, which over-replaces but never suppresses a real change).
	// A non-id-shaped reference is unambiguously a name, since an id is always id-shaped.
	if postgresIdRe.MatchString(plan) {
		return
	}
	// Only the state/response side carries the canonical path; a configured parent is a
	// valid Reference (name or id, no slashes per the openapi Reference pattern), so reduce
	// only the state value and compare it against the trimmed plan name. Reducing the plan
	// too would let a malformed path whose leaf matches a different parent's name suppress
	// a real replace.
	if normalizeParentRef(req.StateValue.ValueString()) == plan {
		resp.PlanValue = req.StateValue
	}
}
