# GeoLibre integration

- **Active**: nothing.
- **Provisioned**: geoserver-style map-server deployment (helm
  `templates/middleware.yaml` `geolibre`, gated; compose `geolibre` service)
  with OGC WMS/WFS conventions and an OTLP endpoint env var.
- **Honest caveat**: the pinned image is a placeholder
  (`geolibre/server:latest`); confirm the correct image and data layout
  against the GeoLibre repository README before enabling. Until then this is
  a structural stub, not a working map server.
