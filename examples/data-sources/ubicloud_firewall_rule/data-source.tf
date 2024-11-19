data "ubicloud_firewall_rule" "example" {
  project_id    = "pj01qy4sty1j7nycv8hfqmgy6t"
  location      = "eu-central-h1"
  firewall_name = "tf-testacc"
  id            = "fr9wf3tra2r23nvsrcm5jkxb8d"
}
output "test_firewall" {
  value = data.ubicloud_firewall_rule.example
}
