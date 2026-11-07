# rbac-go agent guide

`rbac-go` is a single-binary Go replacement for the v1 subset of
insights-rbac that koku and the on-prem UI use. It is a separate git
repository, checked out inside `insights-rbac/`; the parent
[AGENTS.md](../AGENTS.md) still applies where it does not conflict with this
file.

## Layout

| Path | Content |
|---|---|
| [cmd/rbac-go/](cmd/rbac-go/) | `serve`, `migrate`, `seed`, `bootstrap`, `sync` entry points |
| [internal/access/](internal/access/) | The `GET /access/` hot path |
| [internal/contract/](internal/contract/) | koku's permission-string normalization, ported |
| [internal/store/](internal/store/) | Portable SQL schema, SQLite and Postgres dialects, pool settings |
| [internal/server/](internal/server/) | HTTP handlers and routes |
| [doc/](doc/) | Contract, findings, operator swap, live-cluster runbook, load test |
| [scripts/loadtest/](scripts/loadtest/) | Reproducible load test, one results directory per stage |

## Commands

```bash
make test-sqlite     # default suite, :memory: SQLite
make test-postgres   # same suite against a scratch Postgres 16
make lint            # go vet + golangci-lint (v2.15.0)
make build
```

`make test-postgres` expects a scratch Postgres on `127.0.0.1:55432`; see the
comment at the top of the [Makefile](Makefile). Both suites and the linter
must pass before a commit is pushed, because CI runs all three.

## Documentation rules

**Every reference to another document or source file must be a real link.**
A file name in backticks is not a link, even when the path is obvious.

* In this repository, use a relative link:
  `[store.go](../internal/store/store.go)`.
* Outside it, use the canonical upstream URL, with a line anchor when a
  specific range is meant:
  `[view.py:192-221](https://github.com/RedHatInsights/insights-rbac/blob/master/rbac/management/access/view.py#L192-L221)`.
* Generated artifacts are linked where they live, for example
  `[REPORT.md](../scripts/loadtest/results/go-pool/REPORT.md)`.
* When a target has no public location (an untracked local note), say so in
  the sentence instead of inventing a link.

CI runs `lychee --offline` over `doc/**/*.md` and [README.md](README.md). A
missing local link target fails the build; `lychee --offline doc/` is the
quick local check.

Keep the documents separate by purpose: [doc/SPEC.md](doc/SPEC.md) is the
contract, [doc/LOADTEST.md](doc/LOADTEST.md) is the measurement record,
[doc/OPERATOR-SWAP.md](doc/OPERATOR-SWAP.md) is the deployment guide.

## Load-test results

Results are committed, one directory per stage under
[scripts/loadtest/results/](scripts/loadtest/results/). Never edit or re-run
the `baseline/` stage: later stages are measured against it. To add a stage,
use the existing entry points and note the measured commit in `ENV.txt`; see
[doc/LOADTEST.md](doc/LOADTEST.md#reproduce).

## Code conventions

* Portable SQL only: write `?` placeholders and let the store rebind them for
  Postgres. No `DISTINCT ON`, `RETURNING`, JSON operators or `ILIKE`.
* Timestamps are fixed-width UTC TEXT via `store.Now()`; set `created` on
  insert and `modified` on create/update, and return both where the v1 shape
  has them (roles, groups).
* List filters mirror Django: `name_match=partial|exact` (partial default),
  case-insensitive matching, and explicit default-group probes override the
  default exclusion. Validate enums and return 400 instead of ignoring them.
* Every query is tenant-scoped. Never return rows from another tenant.
* `internal/access` always includes the public tenant's platform-default
  groups; admin-default groups only for org admins.
* No cache process. Valkey/Redis environment variables are accepted as
  no-ops so the Helm chart's assertions keep passing.
* Conventional commit messages (`fix(store): ...`), author
  `Martin Povolny <mpovolny@redhat.com>`.
