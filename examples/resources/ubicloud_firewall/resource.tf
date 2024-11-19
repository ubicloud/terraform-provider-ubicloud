variable "project_id" {
  description = "Ubicloud project"
  type        = string
  default     = "pj01qy4sty1j7nycv8hfqmgy6t"
}

variable "location" {
  description = "Ubicloud location"
  type        = string
  default     = "eu-central-h1"
}

resource "ubicloud_firewall" "example" {
  project_id  = var.project_id
  location    = var.location
  name        = "example-firewall"
  description = "Description of firewall"
}

resource "ubicloud_firewall_rule" "ssh" {
  project_id  = var.project_id
  firewall_id = ubicloud_firewall.example.id
  cidr        = "0.0.0.0/0"
  port_range  = "22..22"
}
