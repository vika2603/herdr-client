# Schema snapshot

`herdr-api.schema.json` is the unmodified output of `herdr api schema --json`
from the herdr binary recorded below. It is the single source for every
generated Go type and method wrapper in this module.

| Field | Value |
| --- | --- |
| herdr version | 0.9.1 |
| `protocol` | 22 |
| `schema_version` | 1 |

`method-results.json` maps each request method to the `ResponseResult`
variant it returns. The schema does not carry this relation, so the table is
maintained by hand: it was read out of the herdr handlers for the version
above, and `internal/e2e` then confirmed it by calling 93 of the 103 methods
against a real server and checking the type that came back, with no
disagreements. The generator
refuses to run when a method in the schema has no entry, or when an entry
names an unknown method or result type.

`known-gaps.json` records differences between a running server and this
snapshot that are understood and accepted, so that `herdrcheck` reports them
as known and fails only on new drift.

## Refreshing

The `Track herdr` workflow performs this against each new herdr release and
opens a pull request, so the steps below are for refreshing against a binary
installed locally. A method the release added has no entry yet, which the
generator refuses to run on, so the workflow reads one out of the handlers of
that release with `internal/cmd/herdrsource` and reports where it read it. A
method it cannot settle gets an entry accepting any result, whose wrapper
returns the `Result` interface until someone narrows it.

```bash
just herdr-check            # report what moved before changing anything
just schema-update          # rewrite herdr-api.schema.json from the installed herdr
just gen                    # regenerate *_gen.go
just check                  # build, test, lint, cross-platform type-check, verify the generated code is current
just e2e                    # confirm the result types against a real server
```

After a refresh, update the version table above and add an entry for every new
method to `method-results.json`. The behaviour the schema does not describe
has no automatic guard; the upgrade checklist in `docs/design.md` lists each
such fact and the herdr source file it came from.
