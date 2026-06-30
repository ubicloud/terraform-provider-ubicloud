# Inject terraform-plugin-framework plan modifiers and cross-field validators into
# the postgres resource schema spec. This is the generator-config injection point
# (same jq patch chain main.go uses for computed/optional/required and sensitive), so
# they regenerate into the _gen.go rather than being hand-patched.
#
# UseStateForUnknown is emitted before RequiresReplace on the Computed create-only
# attributes (flavor, parent): on a no-op or unset config the value pins to prior state
# first, so RequiresReplace then compares equal values and does not spuriously force a
# replace. The framework codegen preserves plan-modifier array order. The write-only
# create-only attributes (private_subnet_name, restrict_by_default) are Optional-only, so
# they never go unknown and take RequiresReplace alone (UseStateForUnknown would be a no-op).
def pm($pkg; $fn):
  {
    custom: {
      imports: [ { path: ("github.com/hashicorp/terraform-plugin-framework/resource/schema/" + $pkg) } ],
      schema_definition: ($pkg + "." + $fn + "()")
    }
  };
def usfu($pkg): pm($pkg; "UseStateForUnknown");
def rr($pkg): pm($pkg; "RequiresReplace");

# parent is a create-time reference the user gives as a name (or id), but the API echoes
# it back as the canonical path "/location/<loc>/postgres/<name>". On import, state holds
# the path while config holds the name, so parent's RequiresReplace would force a
# spurious replace. ParentRefStability pins the planned value to prior state when both
# resolve to the same parent; placed BEFORE RequiresReplace so it then compares equal.
# The name form normalizes to the path's name segment so it pins; an id form cannot be
# reduced to the name by this modifier (the response never carries the id, and a plan
# modifier cannot look it up), so it still churns after import. Resolving the id would need
# a computed parent_id plus an extra Read lookup, disproportionate for this niche, so the
# description (set in the parent branch below) records the limitation for users instead.
def parent_stability:
  {
    custom: {
      imports: [ { path: "github.com/ubicloud/terraform-provider-ubicloud/internal/planmodifiers" } ],
      schema_definition: "planmodifiers.ParentRefStability()"
    }
  };

# ConflictsWith(parent): a read replica inherits these from its parent, so the
# read-replica create body (POST .../read-replica) accepts none of them. Setting any
# alongside parent is rejected at plan time rather than silently dropped on dispatch.
def cw($pkg):
  {
    custom: {
      imports: [
        { path: "github.com/hashicorp/terraform-plugin-framework/path" },
        { path: ("github.com/hashicorp/terraform-plugin-framework-validators/" + $pkg) }
      ],
      schema_definition: ($pkg + ".ConflictsWith(path.MatchRoot(\"parent\"))")
    }
  };

# AlsoRequires(parent): restore_target names a point in time but not the database to restore
# FROM. The source is given as parent (the backend records the restored db's parent_id as the
# source), so restore_target without parent is meaningless and is rejected at plan time. The
# source-inherited inputs (size, version, ...) already ConflictsWith(parent), so a restore
# that also sets them is rejected through parent.
def ar($pkg):
  {
    custom: {
      imports: [
        { path: "github.com/hashicorp/terraform-plugin-framework/path" },
        { path: ("github.com/hashicorp/terraform-plugin-framework-validators/" + $pkg) }
      ],
      schema_definition: ($pkg + ".AlsoRequires(path.MatchRoot(\"parent\"))")
    }
  };

# RFC 3339 validity check for restore_target, rejected at plan. A configured-but-blank or
# malformed restore_target would otherwise reach createPostgresRestore and fail only at
# apply (time.Parse) -- or, before postgresHasRestoreTarget was tightened, silently route to
# the read-replica endpoint. validators.RFC3339() runs the SAME time.Parse(time.RFC3339)
# the body builder uses, so plan and apply give the identical verdict: this rejects
# empty/whitespace AND out-of-range values (month 13, hour 24, second 60) a coarse shape
# regex would have let through to fail only on the wire.
def rfc3339:
  {
    custom: {
      imports: [
        { path: "github.com/ubicloud/terraform-provider-ubicloud/internal/validators" }
      ],
      schema_definition: "validators.RFC3339()"
    }
  };

(.resources[] | select(.name == "postgres") | .schema.attributes) |= map(
  # Create-only Computed immutables: pin to state when unset, replace when changed.
  if .name == "flavor" then .string.plan_modifiers = [usfu("stringplanmodifier"), rr("stringplanmodifier")]
  elif .name == "parent" then (.string.plan_modifiers = [usfu("stringplanmodifier"), parent_stability, rr("stringplanmodifier")]
    | .string.description += ". Give the parent database's name or id. The API reports the parent by name, so an imported read replica whose parent is configured by id plans a spurious replace; configure parent by name to avoid it.")
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
  # username is the literal constant "postgres" the serializer always returns, so unlike the
  # other detailed read fields it never converges or rotates: pin it so a no-op/import plan does
  # not churn it as known-after-apply. The convergence-sensitive reads stay UNpinned on purpose
  # (hostname/connection_string embed the in-place-mutable name; vm_size/storage_size_gib
  # converge on an in-place resize with target_* carrying the request; password rotates via the
  # out-of-band reset action; fallback_active tracks the representative server across failover;
  # firewall_rules mirror customer firewall state). Pinning them would show a stale value on a
  # plan about to change it. See postgres_plan_modifiers_test.go (pinned vs floating).
  elif .name == "username" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  elif .name == "ha_type" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  # size and storage_size are computed_optional (a replica omits them and inherits the
  # parent's): pin to prior state so an omitted value does not churn to unknown on a
  # replica re-plan.
  elif .name == "size" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  elif .name == "storage_size" then .int64.plan_modifiers = [usfu("int64planmodifier")]
  # version only changes via the imperative upgrade POST, never on a no-op; pin it so
  # an omitted version does not churn to unknown (and does not trip Update) on an
  # in-place change elsewhere.
  elif .name == "version" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  elif .name == "primary" then .bool.plan_modifiers = [usfu("boolplanmodifier")]
  elif .name == "read_replica" then .bool.plan_modifiers = [usfu("boolplanmodifier")]
  elif .name == "maintenance_window_start_at" then .int64.plan_modifiers = [usfu("int64planmodifier")]
  elif .name == "tags" then .list_nested.plan_modifiers = [usfu("listplanmodifier")]
  elif .name == "pg_config" then .map.plan_modifiers = [usfu("mapplanmodifier")]
  elif .name == "pgbouncer_config" then .map.plan_modifiers = [usfu("mapplanmodifier")]
  else . end
)
# Append ConflictsWith(parent) to the parent-inherited create inputs (kept separate so
# the existing typed validators, e.g. version's OneOf, are preserved, not replaced).
# restrict_by_default and private_subnet_name are included: the read-replica create body
# accepts only {name, pg_config, pgbouncer_config, tags} and a replica inherits the parent's
# subnet and firewall, so setting either alongside parent must fail at validate time, not be
# silently dropped on dispatch (and then retained in state as a value the server never saw).
| (.resources[] | select(.name == "postgres") | .schema.attributes) |= map(
  if .name == "size" or .name == "version" or .name == "ha_type" or .name == "flavor" or .name == "private_subnet_name"
    then .string.validators += [cw("stringvalidator")]
  elif .name == "storage_size" then .int64.validators += [cw("int64validator")]
  elif .name == "restrict_by_default" then .bool.validators += [cw("boolvalidator")]
  # restore_target requires parent (the restore source); the inherited size/version/etc.
  # already ConflictsWith(parent), so a restore that sets them is rejected through parent. The
  # RFC 3339 validity check rejects a blank/malformed target at plan rather than at apply.
  elif .name == "restore_target" then .string.validators += [ar("stringvalidator"), rfc3339]
  else . end
)
# VM resource plan modifiers. gpu is a write-only create-only input (Optional-only; its
# create form "count:type" differs from the display read form and it is never read back into
# state), and the vm resource has no in-place Update. A changed gpu must therefore force a
# replace rather than hit the unsupported Update. RequiresReplace only: gpu is not Computed,
# so it never goes unknown and UseStateForUnknown would be a dead no-op (mirrors postgres's
# write-only private_subnet_name/restrict_by_default). The description records the write-only
# residual: because gpu is never read back, an imported vm has gpu unset and any later gpu
# config (including the degenerate no-gpu form "0:", which the backend normalizes to no gpu)
# plans a replace. The data source surfaces the GPU for reads.
#
# The vm resource has NO in-place Update (vmResource.Update hard-errors), so EVERY create-only
# immutable must RequiresReplace or a changed value plans an unsupported in-place update and
# hard-errors; and every stable read-back computed must UseStateForUnknown so a no-op apply
# does not churn it as "(known after apply)". The three buckets mirror the postgres pass:
#   - Required path identity and Optional-only write-only inputs: never Computed, so they never
#     go unknown -> RequiresReplace alone (UseStateForUnknown would be a dead no-op). gpu and
#     init_script are write-only inputs reclassified to Optional-only (main.go); init_script is
#     never read back (setVmStateResource maps no init_script), so leaving it Computed would let
#     an omitted value plan unknown over a null prior state that usfu cannot pin, and
#     RequiresReplace would then force a spurious replace on a no-op.
#   - Optional+Computed read-back immutables (size, unix_user): the server echoes a non-null
#     value, so UseStateForUnknown pins the omitted value to prior state BEFORE RequiresReplace
#     compares (equal -> no spurious replace). usfu must precede rr, mirroring postgres flavor.
#   - Stable read-back computeds: pin to prior state with UseStateForUnknown. ip4/ip6 named in
#     the issue body do not exist on the resource schema (only ip4_enabled / private_ipv4 /
#     private_ipv6), so they are not modified here.
| (.resources[] | select(.name == "vm") | .schema.attributes) |= map(
  # Path identity (Required): replace if changed.
  if .name == "project_id" then .string.plan_modifiers = [rr("stringplanmodifier")]
  elif .name == "location" then .string.plan_modifiers = [rr("stringplanmodifier")]
  elif .name == "name" then .string.plan_modifiers = [rr("stringplanmodifier")]
  # Create-only write-only immutables (Required or Optional-only: never go unknown).
  elif .name == "public_key" then .string.plan_modifiers = [rr("stringplanmodifier")]
  elif .name == "boot_image" then .string.plan_modifiers = [rr("stringplanmodifier")]
  elif .name == "private_subnet_id" then .string.plan_modifiers = [rr("stringplanmodifier")]
  elif .name == "enable_ip4" then .bool.plan_modifiers = [rr("boolplanmodifier")]
  elif .name == "storage_size" then .int64.plan_modifiers = [rr("int64planmodifier")]
  elif .name == "gpu" then (.string.plan_modifiers = [rr("stringplanmodifier")]
    | .string.description += ". Write-only create input: it is not read back into state, so an imported vm shows gpu unset and setting gpu in config after import forces replacement; read the GPU configuration via the ubicloud_vm data source.")
  elif .name == "init_script" then (.string.plan_modifiers = [rr("stringplanmodifier")]
    | .string.description += ". Write-only create input: it is not read back into state, so an imported vm shows init_script unset and setting it in config after import forces replacement.")
  # Create-only Computed immutables (read back): pin to state when unset, replace when changed.
  elif .name == "size" then .string.plan_modifiers = [usfu("stringplanmodifier"), rr("stringplanmodifier")]
  elif .name == "unix_user" then .string.plan_modifiers = [usfu("stringplanmodifier"), rr("stringplanmodifier")]
  # Stable read-back computeds: pin to prior state so a no-op apply does not churn them.
  elif .name == "id" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  elif .name == "ip4_enabled" then .bool.plan_modifiers = [usfu("boolplanmodifier")]
  elif .name == "private_ipv4" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  elif .name == "private_ipv6" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  elif .name == "subnet" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  elif .name == "storage_size_gib" then .int64.plan_modifiers = [usfu("int64planmodifier")]
  elif .name == "firewalls" then .list_nested.plan_modifiers = [usfu("listplanmodifier")]
  else . end
)
# Firewall resource plan modifiers. firewallResource.Update hard-errors, so every create-only
# immutable must RequiresReplace (else a changed value plans an unsupported in-place update)
# and every stable read-back computed UseStateForUnknown (so a no-op apply does not churn it).
#   - Required path identity (project_id, location, name): rr alone (never Computed).
#   - description is Optional+Computed and read back, so usfu pins an omitted value to prior
#     state BEFORE rr compares (equal, so no spurious replace); usfu must precede rr.
#   - Stable read-back computeds (id, firewall_rules, private_subnets): usfu alone.
| (.resources[] | select(.name == "firewall") | .schema.attributes) |= map(
  if .name == "project_id" then .string.plan_modifiers = [rr("stringplanmodifier")]
  elif .name == "location" then .string.plan_modifiers = [rr("stringplanmodifier")]
  elif .name == "name" then .string.plan_modifiers = [rr("stringplanmodifier")]
  elif .name == "description" then .string.plan_modifiers = [usfu("stringplanmodifier"), rr("stringplanmodifier")]
  elif .name == "id" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  elif .name == "firewall_rules" then .list_nested.plan_modifiers = [usfu("listplanmodifier")]
  elif .name == "private_subnets" then .list_nested.plan_modifiers = [usfu("listplanmodifier")]
  else . end
)
# Private subnet resource plan modifiers. privateSubnetResource.Update hard-errors, so every
# create-only immutable RequiresReplace and every stable read-back computed UseStateForUnknown.
#   - Required path identity (project_id, location, name): rr alone.
#   - firewall_id is the firewall attached at creation (Optional-only write-only input, never
#     read back: the attachment surfaces in the firewalls list). rr alone; it is not Computed,
#     so it never goes unknown and usfu would be a dead no-op.
#   - Stable read-back computeds (id, net4, net6, state, nics, firewalls): usfu alone.
| (.resources[] | select(.name == "private_subnet") | .schema.attributes) |= map(
  if .name == "project_id" then .string.plan_modifiers = [rr("stringplanmodifier")]
  elif .name == "location" then .string.plan_modifiers = [rr("stringplanmodifier")]
  elif .name == "name" then .string.plan_modifiers = [rr("stringplanmodifier")]
  elif .name == "firewall_id" then .string.plan_modifiers = [rr("stringplanmodifier")]
  elif .name == "id" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  elif .name == "net4" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  elif .name == "net6" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  elif .name == "state" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  elif .name == "nics" then .list_nested.plan_modifiers = [usfu("listplanmodifier")]
  elif .name == "firewalls" then .list_nested.plan_modifiers = [usfu("listplanmodifier")]
  else . end
)
# Firewall rule resource plan modifiers. firewallRuleResource.Update hard-errors, so every
# create-only immutable RequiresReplace and every stable read-back computed UseStateForUnknown.
#   - Required path identity (project_id, location): rr alone.
#   - firewall_id and firewall_name are the write-only parent reference (exactly one is set,
#     neither is read back). They are reclassified Optional-only (main.go) so the omitted
#     member stays a known null instead of planning unknown over a null prior state that usfu
#     cannot pin (which would make rr force a spurious replace). rr alone; the description
#     records the write-only residual.
#   - cidr (Required) takes rr alone.
#   - description, port_range and protocol are Optional+Computed and read back, so usfu pins
#     an omitted value to prior state BEFORE rr compares; usfu must precede rr.
#   - id (stable read-back computed): usfu alone.
| (.resources[] | select(.name == "firewall_rule") | .schema.attributes) |= map(
  if .name == "project_id" then .string.plan_modifiers = [rr("stringplanmodifier")]
  elif .name == "location" then .string.plan_modifiers = [rr("stringplanmodifier")]
  elif .name == "firewall_id" then (.string.plan_modifiers = [rr("stringplanmodifier")]
    | .string.description = "ID of the parent firewall. Set either firewall_id or firewall_name. Write-only reference: it is not read back from the API, so changing it forces replacement.")
  elif .name == "firewall_name" then (.string.plan_modifiers = [rr("stringplanmodifier")]
    | .string.description += ". Write-only reference: it is not read back from the API, so changing it forces replacement.")
  elif .name == "cidr" then .string.plan_modifiers = [rr("stringplanmodifier")]
  elif .name == "description" then .string.plan_modifiers = [usfu("stringplanmodifier"), rr("stringplanmodifier")]
  elif .name == "port_range" then .string.plan_modifiers = [usfu("stringplanmodifier"), rr("stringplanmodifier")]
  elif .name == "protocol" then .string.plan_modifiers = [usfu("stringplanmodifier"), rr("stringplanmodifier")]
  elif .name == "id" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  else . end
)
# Project resource plan modifiers. projectResource.Update hard-errors, so the create-only
# immutable must RequiresReplace (a changed name otherwise plans an unsupported in-place
# update and hard-errors) and every stable read-back computed UseStateForUnknown (so a no-op
# apply does not churn it as "known after apply").
#   - name is the Required create/path identity: rr alone (never Computed).
#   - id (string), credit (float64) and discount (int64) are pure read-back computeds: usfu
#     alone. They are not inputs, so no RequiresReplace (a drifting server value must never
#     destroy the project). credit is the first float64 attribute to take a plan modifier.
| (.resources[] | select(.name == "project") | .schema.attributes) |= map(
  if .name == "name" then .string.plan_modifiers = [rr("stringplanmodifier")]
  elif .name == "id" then .string.plan_modifiers = [usfu("stringplanmodifier")]
  elif .name == "credit" then .float64.plan_modifiers = [usfu("float64planmodifier")]
  elif .name == "discount" then .int64.plan_modifiers = [usfu("int64planmodifier")]
  else . end
)
