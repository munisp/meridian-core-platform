module meridian/core-platform/services/admin-api

go 1.23

require (
	github.com/jackc/pgx/v5 v5.5.5
	github.com/munisp/meridian-core-platform/packages/permify-models v0.0.0
)

replace github.com/munisp/meridian-core-platform/packages/permify-models => ../../packages/permify-models

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20221227161230-091c0ba34f0a // indirect
	github.com/jackc/puddle/v2 v2.2.1 // indirect
	golang.org/x/crypto v0.17.0 // indirect
	golang.org/x/sync v0.1.0 // indirect
	golang.org/x/text v0.14.0 // indirect
)
// NOTE: go.sum is intentionally not committed — CI regenerates it
// (go mod tidy) for this module.
