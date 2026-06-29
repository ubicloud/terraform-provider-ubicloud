# Restore the four detailed read fields that terraform-plugin-codegen-openapi v0.3.0
# drops from the postgres RESOURCE schema while keeping them on the DATA SOURCE, even
# though both ops 200-return the identical PostgresDatabase response component
# (allOf[base, detailed]). They are response-only strings; the v0.3.0 resource allOf
# merge silently omits exactly these four (hostname/username/password, also response-only
# nullable strings, survive). Without state the epic's convergence signal is missing from
# the resource, and connection_string/earliest_restore_time/latest_restore_time are absent.
#
# Copy them verbatim from the data source so the resource mirrors the same response
# surface with no hand-maintained shapes. This step runs AFTER the sensitive jq-mod, so
# connection_string carries the sensitive flag set on the data source (it embeds the
# superuser password), and BEFORE plan_modifiers.jq, whose else-branch leaves these four
# untouched (volatile computeds, like target_*, get no UseStateForUnknown).
#
# Idempotent: only names the resource is currently MISSING are appended. If a future
# codegen version stops dropping any of these, the filtered set shrinks accordingly and
# this mod never creates duplicate attribute entries.
(.resources[] | select(.name == "postgres") | .schema.attributes | map(.name)) as $have
| (
    .datasources[] | select(.name == "postgres") | .schema.attributes
    | map(select(
        (.name | IN("state", "connection_string", "earliest_restore_time", "latest_restore_time"))
        and (.name | IN($have[]) | not)
      ))
  ) as $reads
| (.resources[] | select(.name == "postgres") | .schema.attributes) += $reads
