resource "ubicloud_project" "example" {
  name = "ExampleProject"
}
output "example_project" {
  value = ubicloud_project.example
}
