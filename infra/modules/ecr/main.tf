# ECR module: one repository per service name in var.repository_names, each
# with the same lifecycle policy (keep last N images) and scan-on-push
# setting. Parameterized over a set so the dev environment can instantiate
# all three service repos (gateway, text, image) from a single module call
# instead of copy-pasting a repository block per service.

resource "aws_ecr_repository" "this" {
  for_each = var.repository_names

  name                 = each.value
  image_tag_mutability = var.image_tag_mutability

  # This project's cost discipline is destroy-after-every-session, so the
  # repos are never empty at destroy time — without force_delete every
  # `terraform destroy` fails on RepositoryNotEmptyException (found by the
  # first real destroy, 2026-07-11). Images here are reproducible build
  # artifacts (rebuilt by CI/deploy from source), never precious data.
  force_delete = true

  image_scanning_configuration {
    scan_on_push = var.scan_on_push
  }

  tags = merge(var.tags, {
    Name = each.value
  })
}

resource "aws_ecr_lifecycle_policy" "this" {
  for_each = var.repository_names

  repository = aws_ecr_repository.this[each.value].name

  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Keep only the last ${var.keep_last_n_images} images"
      selection = {
        tagStatus   = "any"
        countType   = "imageCountMoreThan"
        countNumber = var.keep_last_n_images
      }
      action = {
        type = "expire"
      }
    }]
  })
}
