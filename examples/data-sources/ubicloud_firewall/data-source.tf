
variable "project_id" {
  description = "Ubicloud project"
  type        = string
  default     = "pj01qy4sty1j7nycv8hfqmgy6t"
}

data "ubicloud_firewall" "example" {
  project_id = var.project_id
  location   = "eu-central-h1"
  name       = "tf-testacc"
}

output "example_firewall" {
  value = data.ubicloud_firewall.example
}
