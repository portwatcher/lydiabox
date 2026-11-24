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

## Known limitations

These limits are known. They are not vulnerabilities for a report. We
intend to remove them in a later version.

- **No egress isolation in the docker fallback.** When `/dev/kvm` is
  absent, the guest runs in a Docker container on the default network,
  and LydiaBox does not filter its outbound traffic. Use the Cloud
  Hypervisor backend on a KVM host for network isolation, or put the
  Docker host behind your own firewall rules.
- **The docker guest runs without `no-new-privileges`.** The guest needs
  passwordless `sudo`, and that option stops it. The container boundary
  is weaker than a VM boundary; use the KVM backend when the agent runs
  code that you do not trust.
- **The LLM tunnel forwards all paths (self-hosted gateway mode).** The
  guest LLM tunnel sends every request path to the magpie gateway with
  the gateway key of the tenant, not only the inference paths. Code in
  the VM can therefore use all routes of that key. Give the gateway only
  the providers and models that the agent must use.
- **Provider base URLs are not limited by destination (self-hosted
  gateway mode).** The owner can set a provider base URL to a loopback,
  link-local, or private address, and the gateway then sends requests
  there. Only set base URLs that you trust, and block the metadata and
  internal addresses of your host at the network level.
- **SQLite files of the guest open by path.** The host checks the path
  of a harness database in the guest home, and then opens it by that path
  string. Code in the VM that changes the file between the two steps can
  make the host open a different file. This applies to all modes that
  read session history from guest databases. Run the host process as an
  unprivileged user that can access only its own data directory.
- **A hard link to a credential file is not detected (guest-native
  mode).** The host guard blocks the known credential paths of each
  harness, but it does not see a hard link to such a file under a
  different name. Do not make links to the sign-in files of a harness in
  the guest home.
- **Token masking covers only known formats.** LydiaBox masks tokens with
  well-known prefixes in stored session events and gateway logs. Other
  secrets that a harness or a tool prints can stay in these copies. Do
  not print secrets in agent sessions, and keep the data directory of the
  host private.
