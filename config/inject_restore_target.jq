# Inject restore_target into the postgres RESOURCE schema: it exists only in the restore
# request body (no create body, no read response), so the codegen never emits it, yet the
# provider needs it on the resource to dispatch a point-in-time restore. Write-only create
# input (Optional, never read back); plan_modifiers.jq attaches its modifiers/validators.
#
# Idempotent (appended only when absent) and guarded: with no postgres resource this is a
# no-op passthrough, never the empty output an unmatched select would produce, which would
# silently wipe every other resource from the generated spec.
if any(.resources[]?; .name == "postgres") then
  (.resources[] | select(.name == "postgres") | .schema.attributes | map(.name)) as $have
  | if ("restore_target" | IN($have[])) then .
    else (.resources[] | select(.name == "postgres") | .schema.attributes) += [
      {
        "name": "restore_target",
        "string": {
          "computed_optional_required": "optional",
          "description": "RFC 3339 timestamp of the point in time to restore to, which must fall within the source database's backup window [earliest_restore_time, latest_restore_time]. Setting it (with parent as the source database) creates this database as a point-in-time restore off the parent instead of a fresh database. The restored database is a full primary, not a read replica, so it can be resized, HA-changed, and version-upgraded in place. Write-only create input: it is not read back from the API, so an imported database shows restore_target unset and setting it in config after import forces replacement."
        }
      }
    ]
    end
else . end
