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

resource "ubicloud_postgres" "example" {
  project_id   = var.project_id
  location     = var.location
  name         = "pg-example"
  size         = "standard-4"
  storage_size = 512

  # Tags are managed only when configured here. If you omit this block, the provider
  # does not track server-side tags, so tags added out of band (including those a
  # point-in-time restore inherits from its parent) stay invisible and never cause a
  # perpetual plan diff. Configure tags to manage and reconcile them.
  tags = [
    { key = "environment", value = "production" },
  ]
}
