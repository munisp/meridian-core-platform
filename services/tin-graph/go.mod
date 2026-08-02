module github.com/munisp/meridian-core-platform/services/tin-graph

go 1.25.0

require (
	github.com/munisp/meridian-core-platform/packages/events v0.0.0
	github.com/munisp/meridian-core-platform/packages/permify-models v0.0.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.10.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/rogpeppe/go-internal v1.15.0 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/text v0.29.0 // indirect
	gopkg.in/yaml.v3 v3.0.1
)

replace github.com/munisp/meridian-core-platform/packages/events => ../../packages/events

replace github.com/munisp/meridian-core-platform/packages/permify-models => ../../packages/permify-models

replace github.com/munisp/meridian-core-platform/packages/rulepack-schema => ../../packages/rulepack-schema
