module github.com/munisp/meridian-core-platform/services/tin-graph

go 1.23.0

require github.com/munisp/meridian-core-platform/packages/events v0.0.0

require github.com/munisp/meridian-core-platform/packages/rulepack-schema v0.0.0

require gopkg.in/yaml.v3 v3.0.1 // indirect

replace github.com/munisp/meridian-core-platform/packages/events => ../../packages/events

replace github.com/munisp/meridian-core-platform/packages/rulepack-schema => ../../packages/rulepack-schema
