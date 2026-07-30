module github.com/munisp/meridian-core-platform/services/rp-registry

go 1.23.0

require github.com/munisp/meridian-core-platform/packages/events v0.0.0
require github.com/munisp/meridian-core-platform/packages/rulepack-schema v0.0.0

replace github.com/munisp/meridian-core-platform/packages/events => ../../packages/events

replace github.com/munisp/meridian-core-platform/packages/rulepack-schema => ../../packages/rulepack-schema
