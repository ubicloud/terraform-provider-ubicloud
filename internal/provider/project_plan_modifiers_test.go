package provider

import (
	"testing"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_project"
)

// projectResource.Update hard-errors, so project is a no-update resource (like vm,
// firewall, private_subnet and firewall_rule). name is the only input and is the
// Required create/path identity, so it must RequiresReplace: a changed name otherwise
// plans an unsupported in-place update and hard-errors instead of a clean replace.
func TestProjectResourceRequiresReplace(t *testing.T) {
	ctx := t.Context()
	s := resource_project.ProjectResourceSchema(ctx)
	attr, ok := s.Attributes["name"]
	if !ok {
		t.Fatal("attribute \"name\" not found in schema")
	}
	descs := postgresPlanModifierDescriptions(ctx, attr)
	if !descsContain(descs, descRequiresReplace) {
		t.Errorf("attribute \"name\": expected RequiresReplace plan modifier, got %v", descs)
	}
	// name is Required and never goes unknown, so UseStateForUnknown would be a dead no-op.
	if descsContain(descs, descUseStateForUnknown) {
		t.Errorf("attribute \"name\": expected NO UseStateForUnknown (Required, never unknown), got %v", descs)
	}
}

// The pure read-back computeds (id string, credit float64, discount int64) must pin to
// prior state so a no-op apply does not churn them as "known after apply". They are not
// inputs, so they must NOT carry RequiresReplace (a server-reported value drifting must
// never destroy the project). credit exercises the float64planmodifier path (new type).
func TestProjectResourceUseStateForUnknown(t *testing.T) {
	ctx := t.Context()
	s := resource_project.ProjectResourceSchema(ctx)
	for _, name := range []string{"id", "credit", "discount"} {
		attr, ok := s.Attributes[name]
		if !ok {
			t.Fatalf("attribute %q not found in schema", name)
		}
		descs := postgresPlanModifierDescriptions(ctx, attr)
		if !descsContain(descs, descUseStateForUnknown) {
			t.Errorf("attribute %q: expected UseStateForUnknown plan modifier, got %v", name, descs)
		}
		if descsContain(descs, descRequiresReplace) {
			t.Errorf("attribute %q: expected NO RequiresReplace (read-back computed), got %v", name, descs)
		}
	}
}
