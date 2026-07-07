# Postgres-only attribute classification plus sensitivity, applied after the shared vm/postgres
# steps so the postgres-specific classes win. Assignment-form filters: idempotent, and a spec
# with no postgres resource matches no paths, so they no-op instead of wiping the output.
def classify($name; $kind; $class):
  (.resources[] | select(.name == "postgres") | .schema.attributes[] | select(.name == $name))[$kind].computed_optional_required = $class;
def sensitive($name):
  ((.resources[], .datasources[]) | select(.name == "postgres") | .schema.attributes[] | select(.name == $name)).string.sensitive = true;

classify("storage_size"; "int64"; "computed_optional")
| classify("size"; "string"; "computed_optional")
| classify("parent"; "string"; "computed_optional")
| classify("private_subnet_name"; "string"; "optional")
| classify("restrict_by_default"; "bool"; "optional")
| classify("maintenance_window_start_at"; "int64"; "computed_optional")
| sensitive("password")
| sensitive("connection_string")
