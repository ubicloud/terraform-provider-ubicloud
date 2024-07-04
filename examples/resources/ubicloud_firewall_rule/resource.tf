
resource "ubicloud_firewall_rule" "ssh" {
  project_id  = "pj01qy4sty1j7nycv8hfqmgy6t"
  firewall_id = "fwk5tac59hjp4mgx1w2s0r4a6v"
  cidr        = "0.0.0.0/0"
  port_range  = "22..22"
}
