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

resource "ubicloud_load_balancer" "example" {
  project_id  = var.project_id
  name        = "example-load-balancer"
  description = "Description of load balancer"
}
