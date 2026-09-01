# Dapr integration

- **Active**: nothing. No service runs a Dapr sidecar today; the event bus is
  direct Redpanda pandaproxy (`packages/events/bus`).
- **Provisioned**: pubsub component (`infra/dapr/components/pubsub.yaml`,
  Kafka/Redpanda), statestore component (Redis), sidecar annotation set
  (`infra/dapr/sidecar-annotations.yaml`), helm CRDs
  (`infra/helm/templates/dapr-components.yaml`, gated on `.Values.dapr.enabled`),
  compose `dapr-placement` service.
- **To activate**: install the Dapr control plane (helm chart `dapr/dapr`),
  apply the components, add the annotation set to a service deployment, then
  switch that service's bus to Dapr building blocks. OTel: daprd exports spans
  to `otel-collector:4317` via the `meridian-tracing` Configuration.
