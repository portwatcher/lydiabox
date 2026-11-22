# Security policy

## Report a vulnerability

Do not open a public issue for a security problem.

Use GitHub private vulnerability reporting:

1. Go to the **Security** tab of this repository.
2. Click **Report a vulnerability**.
3. Write the affected version or commit, the steps to reproduce the
   problem, and the effect.

We read each report and send an answer in the advisory. When a fix is
ready, we publish a security advisory and credit the reporter if the
reporter agrees.

## Supported versions

Only the latest commit on `main` gets security fixes.

## Scope

These areas are in scope:

- the host control plane (`cmd/lydiabox`): authentication, the HTTP and
  WebSocket API, the File API path sandbox, WebDAV, and the S3 gateway;
- secret storage: the AES-256-GCM encrypted connector and OAuth state,
  and the magpie gateway keys;
- isolation between the agent VM and the host: guest RPC, the host broker,
  and the LLM gateway channels;
- the guest daemon (`cmd/lydiabox-guest`).

The agent VM runs code from LLMs and the internet. Code that runs inside
the VM with the permissions of the agent is expected behavior. A way for
that code to read a host secret, reach the host outside the documented
channels, or reach another tenant is a vulnerability.

Problems in third-party components (JuiceFS, magpie, Cloud Hypervisor,
virtiofsd, cloudflared, and the harness CLIs) go to their own projects.
