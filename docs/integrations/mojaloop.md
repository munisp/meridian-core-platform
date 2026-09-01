# Mojaloop integration

- **Active**: nothing.
- **Provisioned**: `mojaloop-simulator` deployment (helm
  `templates/middleware.yaml`, gate `.Values.mojaloop.enabled`; compose
  `mojaloop-simulator`). This is the **simulator** image for interop testing
  of merchant-payment flows.
- **Live vs sim**: no live Mojaloop scheme adapter (ML API adapter, quoting,
  transfers) is wired anywhere; switching to a live switch requires quoting/
  transfer services + DFSP certs and is out of scope for this wave. Any claim
  of "Mojaloop payments working" beyond the simulator would be fabricated.
