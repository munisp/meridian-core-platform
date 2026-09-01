# OpenAppSec integration

- **Active**: nothing. APISIX runs without a WAF layer today.
- **Provisioned**: helm `openappsec` gate (values) + compose `openappsec-agent`
  service; APISIX attachment follows the open-appsec agent model (agent
  container next to the gateway, plugin enabled per-route).
- **To activate**: deploy the open-appsec agent
  (`ghcr.io/openappsec/agent`), register it with the management portal or
  run in local-declarative mode, then enable the APISIX `open-appsec` plugin
  on routes in `infra/apisix/limit-plugins.yaml`. Not done here: enabling the
  plugin without a running agent would break the gateway.
