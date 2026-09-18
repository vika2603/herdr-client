set shell := ["zsh", "-cu"]

default: check

# Regenerate the Go API surface from the schema snapshot.
gen:
    go generate ./...

# Fail when the checked-in generated code differs from the generator output.
check-gen: gen
    git diff --exit-code -- '*_gen.go'

build:
    go build ./...

test:
    go test -race ./...

lint:
    golangci-lint run ./...

# Type-check the e2e suite. Its build tag keeps it out of `test` and
# `.golangci.yml` excludes it, so nothing else notices when a change to the
# client breaks it.
check-e2e:
    go vet -tags e2e ./internal/e2e/...

# Type-check every package, tests included, for the other platforms. `go
# build` skips test files, so a break confined to a platform-specific one
# would otherwise reach CI.
check-cross:
    GOOS=windows go vet ./...
    GOOS=linux go vet ./...
    GOOS=darwin go vet ./...

check: build test lint check-gen check-e2e check-cross

# Live tests against a Herdr server the suite starts itself (see internal/e2e).
# -v keeps the coverage report the suite prints visible on a passing run.
e2e:
    go test -tags e2e -count=1 -v ./internal/e2e/...

# Report how far the installed herdr and the running server have moved from
# the schema snapshot. Exits non-zero on drift that known-gaps.json does not
# already account for.
herdr-check:
    go run ./internal/cmd/herdrcheck

# Read the result type of each method out of a herdr checkout and fill in any
# the table lacks. The schema does not carry this relation, so a release that
# adds a method otherwise stops `just gen`.
methods src:
    go run ./internal/cmd/herdrsource -src {{src}} -apply

# Refresh the schema snapshot from the installed herdr binary.
schema-update:
    herdr api schema --output schema/herdr-api.schema.json
    herdr --version
