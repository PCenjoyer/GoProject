# Security policy

Please report vulnerabilities privately through GitHub Security Advisories rather
than a public issue.

Operational deployments should:

- expose the API behind authentication and tenant-aware authorization;
- keep the diagnostics port private;
- encrypt endpoint secrets at rest with a managed key;
- enforce an outbound network allowlist to mitigate SSRF;
- terminate TLS at the service mesh or ingress;
- rotate endpoint signing secrets and database credentials;
- retain audit logs for endpoint and replay changes.

This repository is a reference implementation and does not claim that its default
local Compose configuration is production-hardened.

