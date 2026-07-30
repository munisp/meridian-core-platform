# CI (manual-move copy)

`ci/workflows/ci.yml` is the canonical CI definition for meridian-core-platform
(HARDENING H6): Go build/vet/test -race per module, pytest per Python service,
admin frontend npm ci/tsc/build, and `cargo check --locked` for geo-rs.

It is stored here because the automation push token may lack the `workflow`
scope required to create files under `.github/workflows/` via the GitHub API.

**Action required (one-time, by a maintainer with workflow scope):**

```bash
mkdir -p .github/workflows
cp ci/workflows/ci.yml .github/workflows/ci.yml
git add .github/workflows/ci.yml && git commit -m "ci: enable workflow" && git push
```

Keep both copies in sync when editing.
