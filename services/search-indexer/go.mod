module github.com/munisp/meridian-core-platform/services/search-indexer

go 1.25.0

require (
	github.com/munisp/meridian-core-platform/packages/events v0.0.0
	github.com/opensearch-project/opensearch-go/v2 v2.3.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.10.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.17.11 // indirect
	github.com/pierrec/lz4/v4 v4.1.22 // indirect
	github.com/twmb/franz-go v1.18.1 // indirect
	github.com/twmb/franz-go/pkg/kmsg v1.9.0 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/text v0.29.0 // indirect
)

replace github.com/munisp/meridian-core-platform/packages/events => ../../packages/events
