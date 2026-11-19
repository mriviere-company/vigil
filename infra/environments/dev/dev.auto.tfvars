# Dev environment overrides, loaded automatically by Terraform (*.auto.tfvars).
#
# Committed on purpose, WITHOUT api_keys: that variable has no default and
# must come from the environment (export TF_VAR_api_keys=...) or -var. A
# *.auto.tfvars value would silently override TF_VAR_* (Terraform variable
# precedence), and keys committed to a public repo are usable by anyone.

aws_region  = "ca-central-1"
name_prefix = "vigil-dev"

# Cost lever (session decision — full reasoning in the ecs-service module's
# capacity_provider variable docstring): FARGATE_SPOT cuts compute cost
# ~70% vs on-demand FARGATE. Acceptable here because the gateway's circuit
# breaker already treats a downstream disappearing as a normal failure mode
# (opens the circuit, degrades to 202 pending instead of hanging) and ECS
# autoscaling + the ecs-service module's deployment circuit breaker replace
# an interrupted Spot task automatically — nothing about this workload
# needs on-demand's stronger placement guarantee.
capacity_provider = "FARGATE_SPOT"

image_tag     = "latest"
model_backend = "stub"

