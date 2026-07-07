# Backstop for the patch chain: an unmatched leading selector emits empty output and silently
# wipes every resource at exit 0, so fail `go generate` loudly instead. Checks the postgres
# resource exists, the attribute-count tripwire (bump on an intended schema change), and the
# injected restore_target and timeouts elements (timeouts lives under .schema.blocks, which
# the attribute count cannot see).
(.resources // []) | map(select(.name == "postgres")) as $p
| if ($p | length) != 1 then
    error("postgres resource missing from the generated spec (patch chain wiped it); got \($p | length) matches")
  else $p[0].schema as $s
  | if ($s.attributes | length) != 36 then
      error("postgres resource has \($s.attributes | length) attributes, expected 36 (chain drop, or intended schema change: update the count in config/assert_postgres_resource.jq)")
    elif (($s.attributes | map(.name) | index("restore_target")) | not) then
      error("postgres resource lost the restore_target attribute (inject_restore_target.jq did not apply)")
    elif ((($s.blocks // []) | map(.name) | index("timeouts")) | not) then
      error("postgres resource lost the timeouts block (inject_timeouts.jq did not apply)")
    else true end
  end
