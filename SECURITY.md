# Security policy

Please report vulnerabilities privately through GitHub Security Advisories rather
than a public issue.

Operational deployments should:

- protect API keys and rotate them through controlled tenant provisioning;
- keep the diagnostics port private;
- store the AES encryption key in a managed secret service and keep old keys during
  any planned rotation;
- retain the built-in SSRF policy and add an infrastructure egress allowlist;
- terminate TLS at the service mesh or ingress;
- rotate endpoint signing secrets and database credentials;
- retain audit logs for endpoint and replay changes.

This repository is a reference implementation and does not claim that its default
local Compose configuration is production-hardened. The
`HOOKFORGE_ALLOW_PRIVATE_ENDPOINTS` override is for local testing only.
