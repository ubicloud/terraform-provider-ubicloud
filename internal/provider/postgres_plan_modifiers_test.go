package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/planmodifiers"
)

// These descriptions are the stable, public Description() strings of the
// upstream RequiresReplace and UseStateForUnknown plan modifiers across every
// type-specific package (stringplanmodifier, boolplanmodifier, ...). Asserting
// on them lets the test verify the generated schema regardless of which
// type-specific modifier the codegen emitted.
const (
	descRequiresReplace    = "If the value of this attribute changes, Terraform will destroy and recreate the resource."
	descUseStateForUnknown = "Once set, the value of this attribute in state will not change."
)

func postgresPlanModifierDescriptions(ctx context.Context, attr schema.Attribute) []string {
	var descs []string
	switch a := attr.(type) {
	case schema.StringAttribute:
		for _, m := range a.PlanModifiers {
			descs = append(descs, m.Description(ctx))
		}
	case schema.BoolAttribute:
		for _, m := range a.PlanModifiers {
			descs = append(descs, m.Description(ctx))
		}
	case schema.Int64Attribute:
		for _, m := range a.PlanModifiers {
			descs = append(descs, m.Description(ctx))
		}
	case schema.Float64Attribute:
		for _, m := range a.PlanModifiers {
			descs = append(descs, m.Description(ctx))
		}
	case schema.ListNestedAttribute:
		for _, m := range a.PlanModifiers {
			descs = append(descs, m.Description(ctx))
		}
	case schema.MapAttribute:
		for _, m := range a.PlanModifiers {
			descs = append(descs, m.Description(ctx))
		}
	}
	return descs
}

func descsContain(descs []string, want string) bool {
	return descsIndex(descs, want) >= 0
}

func descsIndex(descs []string, want string) int {
	for i, d := range descs {
		if d == want {
			return i
		}
	}
	return -1
}

// On attributes carrying both modifiers, UseStateForUnknown must be emitted BEFORE
// RequiresReplace: the framework threads the planned value between modifiers in slice
// order, so pinning to prior state first lets RequiresReplace then compare equal
// values on an unset/no-op and not force a spurious replace. Only the Computed create-only
// attributes (flavor, parent) carry both; the write-only Optional-only ones take rr alone.
func TestPostgresResourceModifierOrder(t *testing.T) {
	ctx := t.Context()
	s := resource_postgres.PostgresResourceSchema(ctx)
	for _, name := range []string{"flavor", "parent"} {
		attr, ok := s.Attributes[name]
		if !ok {
			t.Fatalf("attribute %q not found in schema", name)
		}
		descs := postgresPlanModifierDescriptions(ctx, attr)
		usfu := descsIndex(descs, descUseStateForUnknown)
		rr := descsIndex(descs, descRequiresReplace)
		if usfu < 0 || rr < 0 {
			t.Errorf("attribute %q: expected both modifiers, got %v", name, descs)
			continue
		}
		if usfu > rr {
			t.Errorf("attribute %q: UseStateForUnknown must precede RequiresReplace, got %v", name, descs)
		}
	}
}

// parent carries the ParentRefStability modifier, and it must sit BETWEEN
// UseStateForUnknown and RequiresReplace: the framework threads the planned value
// between modifiers in slice order, so it pins an imported replica's path-form state
// against a name-form config before RequiresReplace compares, suppressing the spurious
// replace. Order is load-bearing, so assert all three positions.
func TestPostgresParentRefStabilityWired(t *testing.T) {
	ctx := t.Context()
	descParentRef := planmodifiers.ParentRefStability().Description(ctx)
	s := resource_postgres.PostgresResourceSchema(ctx)
	attr, ok := s.Attributes["parent"]
	if !ok {
		t.Fatal("attribute \"parent\" not found in schema")
	}
	descs := postgresPlanModifierDescriptions(ctx, attr)
	usfu := descsIndex(descs, descUseStateForUnknown)
	stab := descsIndex(descs, descParentRef)
	rr := descsIndex(descs, descRequiresReplace)
	if usfu < 0 || stab < 0 || rr < 0 {
		t.Fatalf("parent: expected UseStateForUnknown, ParentRefStability, and RequiresReplace; got %v", descs)
	}
	if usfu >= stab || stab >= rr {
		t.Errorf("parent: modifier order must be UseStateForUnknown < ParentRefStability < RequiresReplace; got %v", descs)
	}
}

// The id-form parent residual (walk issue pg-replica-import-parent-id-form-residual) is
// a documented limitation: the API echoes parent only as the name path, so an imported
// replica whose parent is configured by id cannot be reconciled client-side and plans a
// spurious replace. The disposition is to document it on the parent attribute. Lock the
// note (set via config/plan_modifiers.jq) so it cannot silently regress out of the docs.
func TestPostgresParentIdFormLimitationDocumented(t *testing.T) {
	ctx := t.Context()
	s := resource_postgres.PostgresResourceSchema(ctx)
	attr, ok := s.Attributes["parent"]
	if !ok {
		t.Fatal("attribute \"parent\" not found in schema")
	}
	sa, ok := attr.(schema.StringAttribute)
	if !ok {
		t.Fatalf("parent: expected schema.StringAttribute, got %T", attr)
	}
	for _, want := range []string{"configure parent by name", "replace"} {
		if !strings.Contains(sa.MarkdownDescription, want) {
			t.Errorf("parent MarkdownDescription must document the id-form limitation (missing %q); got %q", want, sa.MarkdownDescription)
		}
	}
}

// Create-only immutables that must replace, not error, on change. project_id and
// location are path identity. flavor/parent/restrict_by_default/private_subnet_name
// are absent from the PATCH body (audit Section D/E).
func TestPostgresResourceRequiresReplace(t *testing.T) {
	ctx := t.Context()
	s := resource_postgres.PostgresResourceSchema(ctx)
	for _, name := range []string{
		"flavor", "parent", "restrict_by_default", "private_subnet_name", "project_id", "location",
	} {
		attr, ok := s.Attributes[name]
		if !ok {
			t.Fatalf("attribute %q not found in schema", name)
		}
		descs := postgresPlanModifierDescriptions(ctx, attr)
		if !descsContain(descs, descRequiresReplace) {
			t.Errorf("attribute %q: expected RequiresReplace plan modifier, got %v", name, descs)
		}
	}
}

// Computeds invariant across a no-op must pin to prior state so a no-op apply does
// not churn them as "known after apply". Pinning the unset Optional+Computed inputs
// is what keeps the no-op proposed state equal to prior, preventing the framework
// from marking the rest unknown.
func TestPostgresResourceUseStateForUnknown(t *testing.T) {
	ctx := t.Context()
	s := resource_postgres.PostgresResourceSchema(ctx)

	pinned := []string{
		"id", "created_at", "ca_certificates",
		"flavor", "ha_type", "version",
		"parent", "primary", "read_replica",
		"tags", "pg_config", "pgbouncer_config", "maintenance_window_start_at",
		// username is the literal constant "postgres" in the serializer; it is never read
		// back as anything else and no in-place mutation changes it, so pinning it is always
		// correct and drops one field from the no-op/import churn.
		"username",
	}
	for _, name := range pinned {
		attr, ok := s.Attributes[name]
		if !ok {
			t.Fatalf("attribute %q not found in schema", name)
		}
		descs := postgresPlanModifierDescriptions(ctx, attr)
		if !descsContain(descs, descUseStateForUnknown) {
			t.Errorf("attribute %q: expected UseStateForUnknown plan modifier, got %v", name, descs)
		}
	}

	// Write-only create-only inputs are Optional-only (never read back), so they never go
	// unknown and must NOT carry UseStateForUnknown (it would be a dead no-op). They keep
	// RequiresReplace alone, asserted in TestPostgresResourceRequiresReplace.
	writeOnly := []string{"restrict_by_default", "private_subnet_name"}
	for _, name := range writeOnly {
		attr, ok := s.Attributes[name]
		if !ok {
			t.Fatalf("attribute %q not found in schema", name)
		}
		descs := postgresPlanModifierDescriptions(ctx, attr)
		if descsContain(descs, descUseStateForUnknown) {
			t.Errorf("attribute %q: expected NO UseStateForUnknown (write-only Optional-only), got %v", name, descs)
		}
	}

	// Convergence-sensitive / lifecycle-varying computeds must stay unknown when an
	// in-place change is planned, so they must NOT be pinned (audit H4, Section E).
	// hostname and connection_string embed the in-place-mutable name (a rename moves both:
	// model hostname/connection_string + provider applyPostgresRename); vm_size and
	// storage_size_gib are the actual sizes that converge during an in-place resize while
	// target_vm_size/target_storage_size_gib carry the request; password changes via the
	// out-of-band reset-superuser-password action; fallback_active tracks the representative
	// server, which swaps on failover; firewall_rules mirror customer firewall state that the
	// backend mutates out of band. Pinning any of these would display a stale value on a plan
	// that is about to change it (walk issue pg-stable-computed-reads-usfu: 7 of its 8
	// proposed fields are NOT stable; only username, above, is).
	floating := []string{
		"hostname", "connection_string", "password", "vm_size", "storage_size_gib",
		"target_vm_size", "target_storage_size_gib", "target_version",
		"target_server_count", "fallback_active", "firewall_rules",
	}
	for _, name := range floating {
		attr, ok := s.Attributes[name]
		if !ok {
			t.Fatalf("attribute %q not found in schema", name)
		}
		descs := postgresPlanModifierDescriptions(ctx, attr)
		if descsContain(descs, descUseStateForUnknown) {
			t.Errorf("attribute %q: expected NO UseStateForUnknown (convergence-sensitive), got %v", name, descs)
		}
	}
}

// postgresValidatorDescriptions returns the Description() of each schema validator on an
// attribute, so a test can assert a ConflictsWith(parent) validator is wired (its description
// is "Ensure that if an attribute is set, these are not set: [parent]").
func postgresValidatorDescriptions(ctx context.Context, attr schema.Attribute) []string {
	var descs []string
	switch a := attr.(type) {
	case schema.StringAttribute:
		for _, v := range a.Validators {
			descs = append(descs, v.Description(ctx))
		}
	case schema.BoolAttribute:
		for _, v := range a.Validators {
			descs = append(descs, v.Description(ctx))
		}
	case schema.Int64Attribute:
		for _, v := range a.Validators {
			descs = append(descs, v.Description(ctx))
		}
	}
	return descs
}

// restrict_by_default and private_subnet_name must ConflictsWith(parent). The read-replica
// create body (POST .../read-replica) accepts only {name, pg_config, pgbouncer_config, tags}
// and a replica inherits the parent's subnet and firewall, so these two are not replica
// inputs. Without the validator, setting either alongside parent is accepted at plan time,
// silently dropped on dispatch (postgresReadReplicaBody omits them), and then retained in
// state as a value the server never saw. The other parent-inherited inputs (size, version,
// ha_type, flavor, storage_size) already carry this; these two were the gap.
func TestPostgresWriteOnlyInputsConflictWithParent(t *testing.T) {
	ctx := t.Context()
	s := resource_postgres.PostgresResourceSchema(ctx)
	for _, name := range []string{"restrict_by_default", "private_subnet_name"} {
		attr, ok := s.Attributes[name]
		if !ok {
			t.Fatalf("attribute %q not found in schema", name)
		}
		descs := postgresValidatorDescriptions(ctx, attr)
		found := false
		for _, d := range descs {
			if strings.Contains(d, "parent") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s must carry a ConflictsWith(parent) validator (a read replica rejects it); validators=%v", name, descs)
		}
	}
}
