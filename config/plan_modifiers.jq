# Inject plan modifiers and cross-field validators into the postgres resource spec, so they
# regenerate into the _gen.go rather than being hand-patched.
#
# UseStateForUnknown is emitted BEFORE RequiresReplace on the Computed create-only attributes:
# an unset config pins to prior first, so RequiresReplace compares equal and does not force a
# spurious replace (the codegen preserves plan-modifier array order).
def pm($pkg; $fn):
  {
    custom: {
      imports: [ { path: ("github.com/hashicorp/terraform-plugin-framework/resource/schema/" + $pkg) } ],
      schema_definition: ($pkg + "." + $fn + "()")
    }
  };
def usfu($pkg): pm($pkg; "UseStateForUnknown");
def rr($pkg): pm($pkg; "RequiresReplace");

# The API echoes parent as the canonical path "/location/<loc>/postgres/<name>" while config
# holds a name, so RequiresReplace alone would force a spurious replace after import.
# ParentRefStability (placed BEFORE RequiresReplace) pins matching references to prior; an id
# form cannot be reduced to the name, a limitation the parent description records for users.
def parent_stability:
  {
    custom: {
      imports: [ { path: "github.com/ubicloud/terraform-provider-ubicloud/internal/planmodifiers" } ],
      schema_definition: "planmodifiers.ParentRefStability()"
    }
  };

# ConflictsWith(parent): the read-replica create body accepts none of these; reject at plan
# rather than silently dropping on dispatch.
def parent_xval($pkg; $fn):
  {
    custom: {
      imports: [
        { path: "github.com/hashicorp/terraform-plugin-framework/path" },
        { path: ("github.com/hashicorp/terraform-plugin-framework-validators/" + $pkg) }
      ],
      schema_definition: ($pkg + "." + $fn + "(path.MatchRoot(\"parent\"))")
    }
  };
def cw($pkg): parent_xval($pkg; "ConflictsWith");

# AlsoRequires(parent): restore_target names a point in time; the source database is parent,
# so restore_target without parent is meaningless and rejected at plan.
def ar($pkg): parent_xval($pkg; "AlsoRequires");

# validators.RFC3339() runs the SAME time.Parse the restore body builder uses, so plan and
# apply agree; it rejects blank and out-of-range values a shape regex would let through.
def rfc3339:
  {
    custom: {
      imports: [
        { path: "github.com/ubicloud/terraform-provider-ubicloud/internal/validators" }
      ],
      schema_definition: "validators.RFC3339()"
    }
  };

# validators.NotBlank() rejects a KNOWN blank parent at config validation, replacing the two
# ModifyPlan arms that each re-checked it; null/unknown skip so an interpolated blank still
# defers to Create's apply-time backstop.
def notblank:
  {
    custom: {
      imports: [
        { path: "github.com/ubicloud/terraform-provider-ubicloud/internal/validators" }
      ],
      schema_definition: "validators.NotBlank()"
    }
  };

# Append only validators not already present, so re-applying this filter is a no-op; the
# codegen's own typed validators (e.g. version's OneOf) are preserved.
def add_validators($existing; $news):
  reduce $news[] as $n
    (($existing // []);
     if any(.[]; .custom.schema_definition == $n.custom.schema_definition) then .
     else . + [$n] end);

(.resources[] | select(.name == "postgres") | .schema.attributes) |= map(
  # Create-only Computed immutables: pin to state when unset, replace when changed.
  if .name == "flavor" then .string.plan_modifiers = [usfu("stringplanmodifier"), rr("stringplanmodifier")]
  elif .name == "parent" then (.string.plan_modifiers = [usfu("stringplanmodifier"), parent_stability, rr("stringplanmodifier")]
    | if (.string.description | contains("Give the parent database's name or id")) then .
      else .string.description += ". Give the parent database's name or id. The API reports the parent by name, so an imported read replica whose parent is configured by id plans a spurious replace; configure parent by name to avoid it." end)
  # Create-only write-only immutables: Optional-only (never read back), so an omitted input
  # stays a known null. No UseStateForUnknown (it never goes unknown); RequiresReplace only.
  elif .name == "private_subnet_name" then .string.plan_modifiers = [rr("stringplanmodifier")]
  elif .name == "restrict_by_default" then .bool.plan_modifiers = [rr("boolplanmodifier")]
  # restore_target is the create-only point-in-time restore input (write-only, never read
  # back): RequiresReplace only, like the other write-only immutables.
  elif .name == "restore_target" then .string.plan_modifiers = [rr("stringplanmodifier")]
  # Path identity: replace if changed (no UseStateForUnknown; these are Required).
  elif .name == "project_id" then .string.plan_modifiers = [rr("stringplanmodifier")]
  elif .name == "location" then .string.plan_modifiers = [rr("stringplanmodifier")]
  # Stable computeds: pin to prior state so a no-op apply does not churn them.
  elif .name == "id" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  elif .name == "created_at" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  elif .name == "ca_certificates" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  # username is the serializer's constant "postgres", so pin it.
  elif .name == "username" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  elif .name == "ha_type" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  # size and storage_size are computed_optional (a replica inherits them): pin so an omitted
  # value does not churn to unknown on a replica re-plan.
  elif .name == "size" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  elif .name == "storage_size" then .int64.plan_modifiers = [usfu("int64planmodifier")]
  # version only changes via the upgrade POST; pin so an omitted version does not churn.
  elif .name == "version" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  elif .name == "primary" then .bool.plan_modifiers = [usfu("boolplanmodifier")]
  elif .name == "read_replica" then .bool.plan_modifiers = [usfu("boolplanmodifier")]
  # USFU pins an omitted window to prior, so removing the argument keeps the last value
  # (Terraform cannot clear the window); the description records that set-only contract.
  elif .name == "maintenance_window_start_at" then (.int64.plan_modifiers = [usfu("int64planmodifier")]
    | if (.int64.description | contains("Start hour")) then .
      else .int64.description += ". Start hour (0-23). Once set, removing this argument keeps the last value; Terraform cannot clear the window (unset it out of band)." end)
  elif .name == "tags" then .list_nested.plan_modifiers = [usfu("listplanmodifier")]
  elif .name == "pg_config" then .map.plan_modifiers = [usfu("mapplanmodifier")]
  elif .name == "pgbouncer_config" then .map.plan_modifiers = [usfu("mapplanmodifier")]
  # Reads an in-place update never mutates: pin them (with the Update-tail hold) so a routine
  # plan does not churn them to "known after apply"; an out-of-band edit still shows in the
  # refresh-drift preamble. The restore window moves every second, so its hold is load-bearing.
  # hostname/connection_string and the convergence signals stay UNpinned to render fresh reads.
  elif .name == "firewall_rules" then .list_nested.plan_modifiers = [usfu("listplanmodifier")]
  elif .name == "password" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  elif .name == "earliest_restore_time" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  elif .name == "latest_restore_time" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  else . end
)
# ConflictsWith(parent) goes only on the never-mutated create-only inputs (flavor and the
# write-only private_subnet_name/restrict_by_default). size/storage_size/ha_type/version are
# DELIBERATELY excluded: a restore also carries parent yet is a mutable primary, and a static
# ConflictsWith (config-only) cannot tell it from a locked replica; their constraints live in
# ModifyPlan, which reads prior state and so frees a restored primary.
| (.resources[] | select(.name == "postgres") | .schema.attributes) |= map(
  if .name == "flavor" or .name == "private_subnet_name"
    then .string.validators = add_validators(.string.validators; [cw("stringvalidator")])
  elif .name == "restrict_by_default" then .bool.validators = add_validators(.bool.validators; [cw("boolvalidator")])
  # A KNOWN blank parent is rejected at config validation, before the create/update plan paths.
  elif .name == "parent" then .string.validators = add_validators(.string.validators; [notblank])
  # restore_target requires parent (the restore source) and a valid RFC 3339 value.
  elif .name == "restore_target" then .string.validators = add_validators(.string.validators; [ar("stringvalidator"), rfc3339])
  else . end
)
