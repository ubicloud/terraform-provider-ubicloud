# Inject the restore_target attribute into the postgres RESOURCE schema. restore_target is
# a member of the restorePostgresDatabase request body only; it appears in neither the
# create body (createPostgresDatabase) nor any read response, so terraform-plugin-codegen-
# openapi never emits it. The provider dispatches a create to POST .../postgres/{parent}/
# restore when restore_target is set (a point-in-time restore off the parent source), so the
# attribute must exist on the resource to carry the user's RFC 3339 timestamp.
#
# It is a write-only create input (Optional, never read back): like private_subnet_name it
# is preserved by terraform, not by setPostgresStateResource. plan_modifiers.jq (the next
# step) attaches RequiresReplace and AlsoRequires(parent); a restore inherits size/version/
# etc. from the source, which already ConflictsWith(parent).
#
# Idempotent: only appended when absent, so a future codegen version that begins emitting
# restore_target does not create a duplicate entry.
(.resources[] | select(.name == "postgres") | .schema.attributes | map(.name)) as $have
| if ("restore_target" | IN($have[])) then .
  else (.resources[] | select(.name == "postgres") | .schema.attributes) += [
    {
      "name": "restore_target",
      "string": {
        "computed_optional_required": "optional",
        "description": "RFC 3339 timestamp of the point in time to restore to, which must fall within the source database's backup window [earliest_restore_time, latest_restore_time]. Setting it (with parent as the source database) creates this database as a point-in-time restore off the parent instead of a fresh database. Write-only create input: it is not read back from the API, so an imported database shows restore_target unset and setting it in config after import forces replacement."
      }
    }
  ]
  end
