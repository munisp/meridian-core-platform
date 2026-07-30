# ci/ — dual-path CI mirror (HARDENING H6)

`ci/workflows/ci.yml` is a byte-identical copy of `.github/workflows/ci.yml`.

H6 push rule: attempt the push to `.github/workflows/ci.yml` first; if the
GitHub API rejects it with a workflow-scope error (403/422 — the token lacks
the `workflow` scope), the workflow lives here instead and a maintainer with
sufficient scope must move it manually:

```sh
mkdir -p .github/workflows && cp ci/workflows/ci.yml .github/workflows/ci.yml
```

Pipeline coverage: Go modules (build/vet/test -race), Python services
(pytest + compile smoke), admin frontend (npm ci, tsc, build), and
`cargo check --locked` for geo-rs.
