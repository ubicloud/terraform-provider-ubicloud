data "ubicloud_firewall_rule" "example" {
  project_id  = "pj01qy4sty1j7nycv8hfqmgy6t"
  firewall_id = "fwk5tac59hjp4mgx1w2s0r4a6v"
  id          = "fr9wf3tra2r23nvsrcm5jkxb8d"
}
output "test_firewall" {
  value = data.ubicloud_firewall_rule.example
}
