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

variable "ssh_key" {
  description = "Public SSH key"
  type        = string
  default     = "theactualpublicsshkey"
}

resource "ubicloud_firewall" "example" {
  project_id  = var.project_id
  name        = "example-firewall"
  description = "Example firewall"
}

resource "ubicloud_firewall_rule" "ssh" {
  project_id  = var.project_id
  firewall_id = ubicloud_firewall.example.id
  cidr        = "0.0.0.0/0"
  port_range  = "22..22"
}

resource "ubicloud_private_subnet" "example" {
  project_id  = var.project_id
  location    = var.location
  firewall_id = ubicloud_firewall.example.id
  name        = "example-subnet"
}

resource "ubicloud_vm" "example" {
  project_id        = var.project_id
  location          = var.location
  public_key        = var.ssh_key
  private_subnet_id = ubicloud_private_subnet.example.id
  name              = "vm-example"
  unix_user         = "ubi"
  size              = "standard-4"
  storage_size      = 80
  enable_ip4        = "true"
  boot_image        = "ubuntu-noble"
}

output "example_vm" {
  value = ubicloud_vm.example
}
