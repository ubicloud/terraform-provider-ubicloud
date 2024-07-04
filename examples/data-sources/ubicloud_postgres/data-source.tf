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

data "ubicloud_postgres" "example" {
  project_id = var.project_id
  location   = var.location
  name       = "pg-example"
}

output "example_postgres" {
  value = data.ubicloud_postgres.example
}