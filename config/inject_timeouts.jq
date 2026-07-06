# Inject a timeouts block so the generated PostgresModel carries the Timeouts field: the
# framework's struct-to-object conversion is strict (a schema block with no model field is a
# hard error), so the field must be generated, not hand-added to the gitignored _gen.go. The
# hand-written Schema() replaces the generated block with timeouts.Block(ctx, Opts{...}).
#
# Idempotent (added only when absent) and guarded: with no postgres resource this is a no-op
# passthrough, never the empty output an unmatched select would produce, which would silently
# wipe every other resource from the generated spec.
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
