
resource "ubicloud_firewall_rule" "ssh" {
  project_id    = "pj01qy4sty1j7nycv8hfqmgy6t"
  location      = "eu-central-1"
  firewall_name = "tf-testacc"
  cidr          = "0.0.0.0/0"
  port_range    = "22..22"
}
