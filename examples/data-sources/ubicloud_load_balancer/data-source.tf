
variable "project_id" {
  description = "Ubicloud project"
  type        = string
  default     = "pj01qy4sty1j7nycv8hfqmgy6t"
}

data "ubicloud_load_balancer" "example" {
  project_id = var.project_id
  id         = "1bk5tac59hjp4mgx1w2s0r4a6v"
}

output "example_load_balancer" {
  value = data.ubicloud_load_balancer.example
}
