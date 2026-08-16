module github.com/munisp/meridian-core-platform/services/ledger

go 1.25.0

require (
	github.com/munisp/meridian-core-platform/packages/events v0.0.0
	github.com/munisp/meridian-core-platform/packages/temporal-sdkx v0.0.0
	github.com/tigerbeetle/tigerbeetle-go v0.16.17
)

require (
	github.com/klauspost/compress v1.17.11 // indirect
	github.com/pierrec/lz4/v4 v4.1.22 // indirect
	github.com/twmb/franz-go v1.18.1 // indirect
	github.com/twmb/franz-go/pkg/kmsg v1.9.0 // indirect
)

replace github.com/munisp/meridian-core-platform/packages/events => ../../packages/events

replace github.com/munisp/meridian-core-platform/packages/temporal-sdkx => ../../packages/temporal-sdkx
