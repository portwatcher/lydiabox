# Cloud Hypervisor test on KVM

This procedure boots the LydiaBox guest with the `chv` driver on a real KVM
host and checks it through the API. It works on any Linux host with
`/dev/kvm`. On a Mac with Apple silicon (M3 or later, macOS 15 or later),
use a Lima VM with nested virtualization (step 1). The results of the run
on 2026-11-10 are in [conformance.md](conformance.md#integration-run-chv-on-kvm).

## 1. KVM host

On a Linux host with KVM, skip to step 2.

On a Mac, install Lima and create an Ubuntu 24.04 arm64 VM with the `vz`
type and nested virtualization. Mount the repository writable:

```sh
brew install lima
cat >lydiabox-kvm.yaml <<EOF
vmType: vz
nestedVirtualization: true
arch: aarch64
cpus: 8
memory: 12GiB
disk: 80GiB
images:
- location: "https://cloud-images.ubuntu.com/releases/noble/release/ubuntu-24.04-server-cloudimg-arm64.img"
  arch: "aarch64"
mounts:
- location: "$PWD"
  writable: true
mountType: virtiofs
containerd:
  system: false
  user: false
EOF
limactl create --name=lydiabox-kvm --tty=false lydiabox-kvm.yaml
limactl start lydiabox-kvm
limactl shell lydiabox-kvm -- ls -l /dev/kvm     # must exist
```

Run the next steps in the VM (`limactl shell lydiabox-kvm`), in the
mounted repository directory.

## 2. Packages

```sh
sudo apt-get update
sudo apt-get install -y docker.io docker-buildx fuse3 nftables e2fsprogs \
  squashfs-tools cpio curl make jq iproute2
sudo usermod -aG docker,kvm "$USER"     # log in again after this
```

Install Go (the version in `go.mod`) from <https://go.dev/dl/> if you build
the binaries in the VM. Alternatively, run `make release` on the Mac and
use `dist/linux-arm64/` through the mount.

## 3. Build

```sh
export LYDIABOX_DATA_DIR=/var/lib/lydiabox
sudo -E deploy/rootfs/build-artifacts.sh --slim --out "$LYDIABOX_DATA_DIR/runtime/images"
make release        # or: make build-host build-guest GOOS=linux GOARCH=arm64 DIST=dist/linux-arm64
sudo install -D -m 0755 dist/linux-arm64/lydiabox /opt/lydiabox/lydiabox
sudo install -D -m 0755 dist/linux-arm64/lydiabox-guest /opt/lydiabox/lydiabox-guest
sudo -E /opt/lydiabox/lydiabox runtime install-local virtiofsd dist/virtiofsd-linux-arm64
```

`build-artifacts.sh` builds virtiofsd (on arm64), the kernel (Cloud
Hypervisor fork, `ch_defconfig` plus `deploy/rootfs/kernel.config`), the
initramfs, and `rootfs.squashfs` from the `desktop-slim` stage. On amd64,
`lydiabox up` downloads virtiofsd, so the `install-local` line is not
necessary. `lydiabox up` downloads `cloud-hypervisor`, `juicefs`, and
`magpie`.

## 4. MinIO and `lydiabox up`

```sh
sudo docker run -d --name lydiabox-kvm-minio --restart unless-stopped \
  -p 127.0.0.1:9000:9000 -v /var/lib/minio:/data \
  -e MINIO_ROOT_USER=lydiaminio -e MINIO_ROOT_PASSWORD=lydiaminio-secret-1 \
  ghcr.io/coollabsio/minio server /data

sudo env LYDIABOX_DATA_DIR=/var/lib/lydiabox LYDIABOX_LISTEN_ADDR=0.0.0.0:8080 \
  LYDIABOX_S3_ENDPOINT=http://127.0.0.1:9000 LYDIABOX_S3_REGION=us-east-1 \
  LYDIABOX_S3_BUCKET=lydiabox-kvm LYDIABOX_S3_ACCESS_KEY=lydiaminio \
  LYDIABOX_S3_SECRET_KEY=lydiaminio-secret-1 LYDIABOX_VM_DRIVER=chv \
  LYDIABOX_VM_CPUS=2 LYDIABOX_VM_MEM_MB=2048 LYDIABOX_IDLE_TIMEOUT=60s \
  LYDIABOX_AUTH_TOKEN=kvm-test-token-0123456789abcdef \
  /opt/lydiabox/lydiabox up
```

`lydiabox up` must run as root: Cloud Hypervisor creates the TAP device,
and the driver installs the nftables rules. Under nested virtualization,
use 2 vCPUs. With 4 vCPUs on a loaded Mac, a boot took more than the
120 s boot limit.

## 5. API checks

In a second shell in the VM:

```sh
T=kvm-test-token-0123456789abcdef
api() { curl -sS -X "$1" -H "Authorization: Bearer $T" -H 'Content-Type: application/json' \
  ${3:+-d "$3"} "http://127.0.0.1:8080$2"; echo; }
```

Terminal checks use a WebSocket client with the subprotocol
`lydiabox.pty.v1` (see [api.md](api.md)): `POST /api/v1/sessions`
`{"harness":"shell"}`, then send each command as a binary frame to
`/api/v1/sessions/{id}/ws` and read the binary output frames.
The web UI terminal (Studio page) also works.

| # | Check | Command | Expected |
| :--- | :--- | :--- | :--- |
| 1 | Wake | `api POST /api/v1/vm/wake` | `"state":"RUNNING"`, `"driver":"chv"` |
| 2 | Kernel and user | `uname -a; id` | `aarch64` (or `x86_64`), `uid=1000(agent)` |
| 3 | Mounts | `mount \| grep -E 'virtiofs\|overlay'` | `overlay on /`, `lydia_home on /home/agent`, `lydia_workspace on /workspace` |
| 4 | CPUs | `grep -c processor /proc/cpuinfo` | `LYDIABOX_VM_CPUS` |
| 5 | Guest to host files | `echo hi >/workspace/a.txt`, then `api GET '/api/v1/fs/workspace/text?path=a.txt'` | content `hi` |
| 6 | Host to guest files | `api PUT '/api/v1/fs/workspace/text?path=b.txt' '{"content":"x\n"}'`, then `cat /workspace/b.txt` | `x` |
| 7 | Internet | `curl -sI https://example.com` | `HTTP/2 200` |
| 8 | Host networks blocked | `curl -m 5 http://<host LAN IP>:8080/`, `curl -m 5 http://172.30.0.1:8080/`, `curl -m 5 http://172.17.0.1:9000/` | time out |
| 9 | LLM gateway tunnel (vsock 9002) | `curl http://127.0.0.1:3425/v1/models` | magpie JSON |
| 10 | Host broker (vsock 9001) | `printf 'protocol=https\nhost=github.com\n\n' \| lydiabox-guest git-cred get; echo $?` | empty output, `0` (broker answers 404: no account). With `LYDIABOX_HOST_ADDR=vsock://2:9555` it fails |
| 11 | Root persistence | `echo x \| sudo tee /etc/lydiabox-kvm.txt`; `api POST /api/v1/vm/stop`; `api POST /api/v1/vm/wake`; `cat /etc/lydiabox-kvm.txt` | `x` |
| 12 | Hibernate | `api POST /api/v1/vm/hibernate`; `curl --unix-socket $DATA/run/vm/default/chv.sock http://localhost/api/v1/vm.info` | `PAUSED`, `"state":"Paused"` |
| 13 | Resume | `api POST /api/v1/vm/wake`; type in the same session | the shell variables are still set |
| 14 | Desktop | `api PUT /api/v1/desktop/mode '{"mode":"TAKEOVER"}'`, then `OBSERVER`; `pgrep -a Xvfb` | `200`; `Xvfb :0` runs |
| 15 | Idle timeout | `DELETE /api/v1/sessions/{id}` for every session, wait | `PAUSED` about 60 s after the last lease |
| 16 | Stop | `api POST /api/v1/vm/stop`; `pgrep cloud-hypervisor virtiofsd`; `nft list tables`; `ip link` | `STOPPED`; no processes, no `lydiabox_*` table, no `lbx*` TAP |

Logs: `$LYDIABOX_DATA_DIR/run/vm/default/serial.log` (guest console,
supervisord) and `cloud-hypervisor.log`. For kernel messages, remove
`quiet` from `Cmdline` in `internal/vmm/chv/payload.go`.

## 6. Clean up

```sh
sudo pkill -TERM -x lydiabox         # graceful shutdown
sudo docker stop lydiabox-kvm-minio
exit
limactl stop lydiabox-kvm            # limactl delete lydiabox-kvm removes it
```

## Not covered

- GPU passthrough (VFIO): the Lima VM has no PCI GPU to pass through.
- WebRTC video in a browser through the forwarded UDP port.
