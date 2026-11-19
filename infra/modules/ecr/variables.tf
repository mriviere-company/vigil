variable "repository_names" {
  type        = set(string)
  description = "One ECR repository per name in this set (e.g. [\"vigil-gateway\", \"vigil-text\", \"vigil-image\"]). A set, not a list, so for_each gives stable per-repository addresses regardless of ordering."
}

variable "image_tag_mutability" {
  type        = string
  description = <<-EOT
    "MUTABLE" (default) lets a tag like `:latest` be overwritten by
    successive pushes — the simplest thing for a dev environment where
    scripts/bootstrap-backend.sh-style manual pushes are the norm and
    Phase 6's CI deploy workflow tags by commit SHA anyway. "IMMUTABLE" is
    the better production default (guarantees a tag always points at the
    same digest) once every push goes through CI with unique tags.
  EOT
  default     = "MUTABLE"

  validation {
    condition     = contains(["MUTABLE", "IMMUTABLE"], var.image_tag_mutability)
    error_message = "image_tag_mutability must be either \"MUTABLE\" or \"IMMUTABLE\"."
  }
}

variable "keep_last_n_images" {
  type        = number
  description = "Number of most-recent images the lifecycle policy keeps per repository; everything older is expired."
  default     = 5
}

variable "scan_on_push" {
  type        = bool
  description = "Enable ECR basic image scanning (free) on every push."
  default     = true
}

variable "tags" {
  type        = map(string)
  description = "Common tags applied to every repository this module creates."
  default     = {}
}
