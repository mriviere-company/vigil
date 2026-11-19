# Repo settings — manual checklist (do once, at publication)

This repo has no GitHub remote yet, so none of this can be clicked through
until it's pushed. Everything below is a one-time setup step for whoever
publishes it (`OWNER` = the real GitHub org/user, e.g. replacing the
`OWNER/vigil-api` placeholder used in the README badge and the OIDC trust
policy below).

## 1. Secret scanning + push protection (free on public repos)

**Settings → Security → Advanced Security**

- Enable **Secret scanning**.
- Enable **Push protection** (blocks a push that contains a recognizable
  secret pattern, before it ever lands in history).

Both are free for public repositories; this is also the acceptance-test
mechanism named in PLAN.md Phase 6 ("push de test avec un faux secret
bloqué").

## 2. Branch protection on `main`

**Settings → Branches → Add branch protection rule** (pattern: `main`)

- Require a pull request before merging.
- **Require status checks to pass before merging**, and require branches to
  be up to date. Required checks (job names from `.github/workflows/ci.yml`):
  - `go`
  - `python (inference/text)`
  - `python (inference/image)`
  - `python (inference/common)`
  - `terraform`
  - `images (gateway)`
  - `images (text)`
  - `images (image)`
  - `gitleaks`
- Do not allow bypassing the above settings (including for admins), so the
  "PR de test montrant un échec de lint bloqué" acceptance item actually
  blocks.

## 3. Repo variables and secret used by `deploy.yml`

**Settings → Secrets and variables → Actions**

Variables tab (non-secret, visible in logs — fine for a role ARN/region):

| Name | Example value | Notes |
|---|---|---|
| `AWS_DEPLOY_ROLE_ARN` | `arn:aws:iam::123456789012:role/vigil-api-github-deploy` | IAM role assumed via OIDC (see §5). Required — `deploy.yml` has no fallback. |
| `AWS_REGION` | `ca-central-1` | Optional; `deploy.yml` defaults to `ca-central-1` if unset (`vars.AWS_REGION \|\| 'ca-central-1'`). |

Secrets tab:

| Name | Value | Notes |
|---|---|---|
| `VIGIL_API_KEYS` | e.g. `real-free-key:free,real-pro-key:pro` | Passed as `TF_VAR_api_keys` / `-var="api_keys=..."` into `terraform apply`/`destroy`. **Never reuse** the committed `dev-free`/`dev-pro` placeholders from `infra/environments/dev/dev.auto.tfvars` here. |

## 4. GitHub OIDC → AWS role (no static AWS keys)

One-time, per AWS account:

1. **Create the GitHub OIDC identity provider** (skip if the account
   already has one — there is only ever one per account):
   ```
   aws iam create-open-id-connect-provider \
     --url https://token.actions.githubusercontent.com \
     --client-id-list sts.amazonaws.com \
     --thumbprint-list 6938fd4d98bab03faadb97b34396831e3780aea1
   ```
2. **Know which `sub` claim format this repo will get.** GitHub is
   cutting over to *immutable subject claims*: repositories **created after
   2026-07-15** (or any repo/org opted in earlier) mint OIDC tokens whose
   `sub` embeds stable numeric IDs instead of the rename-able owner/repo
   slugs. Since this repo gets published right around that date, do not
   assume either format — the two shapes are:

   | | `sub` format |
   |---|---|
   | Legacy (repo created before the cutover, not opted in) | `repo:<OWNER>/vigil:ref:refs/heads/main` |
   | Immutable (created after 2026-07-15, or opted in) | `repo:<OWNER>@<owner-id>/vigil@<repo-id>:ref:refs/heads/main` |

   **Mandatory verification before trusting either** — the failure mode of
   guessing wrong is a *silent* `AccessDenied` on
   `sts:AssumeRoleWithWebIdentity` (the configure-aws-credentials step just
   fails with "Not authorized to perform sts:AssumeRoleWithWebIdentity";
   nothing tells you the `sub` pattern in the trust policy is what didn't
   match). Run this one-off step in any workflow with
   `permissions: id-token: write` (dispatch it once, read the printed
   claims, delete the step):

   ```yaml
   - name: Print OIDC token claims (one-off — delete after verifying)
     run: |
       JWT=$(curl -s -H "Authorization: bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" \
         "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=sts.amazonaws.com" | jq -r '.value')
       PAYLOAD=$(echo "$JWT" | cut -d. -f2 | tr '_-' '/+')
       pad=$(( (4 - ${#PAYLOAD} % 4) % 4 ))
       if [ "$pad" -gt 0 ]; then PAYLOAD="$PAYLOAD$(printf '=%.0s' $(seq 1 "$pad"))"; fi
       echo "$PAYLOAD" | base64 -d 2>/dev/null | jq '{sub, aud, ref, repository, repository_id, repository_owner_id}'
   ```
   (JWT payloads are base64**url** — `-`/`_` instead of `+`/`/`, and unpadded;
   `base64 -d` needs the standard alphabet plus `=` padding restored first,
   hence the `tr` + padding line above.)

3. **Create the deploy role** with this trust policy (placeholders:
   `<AWS_ACCOUNT_ID>`, `<OWNER>`, `<owner-id>`, `<repo-id>` — the last two
   come from the verification step above, as `repository_owner_id` and
   `repository_id`). Both `sub` patterns are listed (a `StringLike` value
   array is an OR); after the verification step confirms which one your
   tokens actually carry, **delete the other** — a trust policy shouldn't
   keep a pattern no real token can ever match:
   ```json
   {
     "Version": "2012-10-17",
     "Statement": [
       {
         "Effect": "Allow",
         "Principal": {
           "Federated": "arn:aws:iam::<AWS_ACCOUNT_ID>:oidc-provider/token.actions.githubusercontent.com"
         },
         "Action": "sts:AssumeRoleWithWebIdentity",
         "Condition": {
           "StringEquals": {
             "token.actions.githubusercontent.com:aud": "sts.amazonaws.com"
           },
           "StringLike": {
             "token.actions.githubusercontent.com:sub": [
               "repo:<OWNER>/vigil:ref:refs/heads/main",
               "repo:<OWNER>@<owner-id>/vigil@<repo-id>:ref:refs/heads/main"
             ]
           }
         }
       }
     ]
   }
   ```
   - **Least privilege on the `sub` claim**: the condition above only
     admits tokens minted for workflow runs triggered *on `main`* in this
     exact repo — matches how `deploy.yml` is meant to be dispatched
     (manually, against `main`). Broadening it (e.g. `repo:<OWNER>/vigil:*`
     to allow dispatch from other branches/tags) trades that scoping away;
     only do it if you actually need to deploy from non-`main` refs.
   - The **permissions policy** attached to this role (what it's allowed to
     *do* in AWS — ECR push, ECS/ALB/VPC/IAM-for-Fargate-execution-role,
     S3+DynamoDB for the Terraform backend, etc.) is intentionally **out of
     scope here** — see `infra/README.md` and the resources each Terraform
     module creates for the full shape of what the role needs.

## 5. CI badge

Done: `README.md`'s badge already points at `mriviere-company/vigil`
(updated once the org/repo name was known, ahead of the actual push) —
nothing left to do here.
