
variable "project_id" {
  description = "Ubicloud project"
  type        = string
  default     = "pj01qy4sty1j7nycv8hfqmgy6t"
}

data "ubicloud_firewall" "example" {
  project_id = var.project_id
  id         = "fwk5tac59hjp4mgx1w2s0r4a6v"
}

output "example_firewall" {
  value = data.ubicloud_firewall.example
}
