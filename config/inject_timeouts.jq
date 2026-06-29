# Inject a timeouts block into the postgres RESOURCE schema. The wait-for-ready behavior
# needs a user-configurable create/update/delete timeout, expressed with
# terraform-plugin-framework-timeouts. That helper's block is not an OpenAPI field, so
# terraform-plugin-codegen-openapi never emits it; inject it here at the framework-IR layer
# (the same patch chain as inject_restore_target.jq) so the generated PostgresModel carries
# the matching Timeouts timeouts.Value field. The framework's struct-to-object conversion
# is strict (a schema block with no corresponding model field is a hard error), so the
# field must be generated rather than hand-added to the gitignored _gen.go.
#
# The block is generated as a SingleNestedBlock with CustomType timeouts.Type{}; the
# hand-written Schema() then REPLACES it with timeouts.Block(ctx, Opts{...}) to attach the
# helper's duration validators and descriptions. The generated block exists to force the
# model field plus the import; its companion TimeoutsType/TimeoutsValue are unused.
#
# Idempotent: only added when absent, so a future codegen version that begins emitting a
# timeouts block does not create a duplicate.
#
# Guarded: when the spec carries no postgres resource the filter is a no-op passthrough (.),
# never the empty output that an unmatched `select(...) as $x | ...` would produce, which would
# silently wipe every other resource from the generated spec.
if any(.resources[]?; .name == "postgres") then
  (.resources[] | select(.name == "postgres") | .schema.blocks // [] | map(.name)) as $have
  | if ("timeouts" | IN($have[])) then .
    else (.resources[] | select(.name == "postgres") | .schema.blocks) += [
      {
        "name": "timeouts",
        "single_nested": {
          "custom_type": {
            "import": { "path": "github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts" },
            "type": "timeouts.Type{}",
            "value_type": "timeouts.Value"
          },
          "attributes": [
            { "name": "create", "string": { "computed_optional_required": "optional" } },
            { "name": "update", "string": { "computed_optional_required": "optional" } },
            { "name": "delete", "string": { "computed_optional_required": "optional" } }
          ]
        }
      }
    ]
    end
else . end
