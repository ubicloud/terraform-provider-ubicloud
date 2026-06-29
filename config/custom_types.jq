# Attach this provider's custom string types (internal/customtypes) to attributes whose
# API value is canonicalized server-side, so the planned and returned forms compare equal
# via the type's semantic-equality logic. This is the generator-config injection point
# (same jq patch chain main.go uses for computed/optional/required and plan modifiers), so
# the custom type regenerates into the _gen.go schema and model rather than being
# hand-patched.
#
# firewall_rule.port_range: the backend stores a port range as a Postgres int4range and
# serializes an equal-bounds range back collapsed to a single port
# (model/firewall_rule.rb display_port_range), so "22..22" round-trips as "22".
# PortRangeType treats the two forms as equal, so an Optional+Computed port_range applies
# without "inconsistent result after apply" and re-plans clean.
def port_range_custom_type:
  {
    import: { path: "github.com/ubicloud/terraform-provider-ubicloud/internal/customtypes" },
    type: "customtypes.PortRangeType{}",
    value_type: "customtypes.PortRangeValue"
  };

(.resources[] | select(.name == "firewall_rule") | .schema.attributes) |= map(
  if .name == "port_range" then .string.custom_type = port_range_custom_type
  else . end
)
