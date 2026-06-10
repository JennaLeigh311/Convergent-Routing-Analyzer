#!/usr/bin/env sh
# Apply (or re-apply) the branch-protection rule on `main` — issue #7, §R7.
#
# Run this AFTER the CI workflow (.github/workflows/ci.yml) has run at least once
# on `main`, so the `CI passed` status check exists and can be marked required.
# Requires `gh` authenticated as a repo admin. Idempotent: the PUT replaces the
# rule wholesale, so re-running is safe.
#
# What it enforces:
#   - require a pull request before merging  -> direct pushes to main are blocked
#     (required_pull_request_reviews present), with 0 required approvals so a
#     solo owner can still merge their own PR (no self-approval deadlock)
#   - require the single `CI passed` status check, branches up to date (strict)
#     -> a PR can't merge unless both CI lanes (Lane A + Lane B) are green
#   - enforce on admins too; no force-pushes, no branch deletion
#
# Required-check contract: we require ONE check, `CI passed` (the aggregate job
# in ci.yml). GitHub reports a check context as the JOB name, so this string must
# match the `name:` of the `ci-passed` job. After applying, verify the real
# reported names with:
#   gh api repos/<owner>/<repo>/commits/main/check-runs -q '.check_runs[].name'
set -eu

REPO="JennaLeigh311/Convergent-Routing-Analyzer"

gh api -X PUT "repos/$REPO/branches/main/protection" \
  -H "Accept: application/vnd.github+json" \
  -F "required_status_checks[strict]=true" \
  -F "required_status_checks[checks][][context]=CI passed" \
  -F "enforce_admins=true" \
  -F "required_pull_request_reviews[required_approving_review_count]=0" \
  -F "restrictions=null" \
  -F "allow_force_pushes=false" \
  -F "allow_deletions=false"

echo "branch protection applied to $REPO@main (required check: 'CI passed')."
echo "verify: open a throwaway PR and confirm 'CI passed' shows as Required and gates merge,"
echo "and that a direct 'git push origin main' is rejected."
