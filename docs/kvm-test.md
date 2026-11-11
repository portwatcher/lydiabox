# Cloud Hypervisor test on KVM

This procedure boots the LydiaBox guest with the `chv` driver on a real KVM
host and checks it through the API. It works on any Linux host with
`/dev/kvm`. On a Mac with Apple silicon (M3 or later, macOS 15 or later),
use a Lima VM with nested virtualization (step 1). The results of the run
on 2026-11-10 are in [conformance.md](conformance.md#integration-run-chv-on-kvm);
the amd64 results (step 9) are in
[conformance.md](conformance.md#integration-run-chv-on-kvm-amd64).

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

## 7. Full desktop image

Steps 3 to 5 use the `desktop-slim` image. To check Chrome, Blender, and
the harness CLIs, build the full `desktop` image: run `build-artifacts.sh`
without `--slim`. The image has a different root, so remove the root
overlay of the slim image first: with `lydiabox up` running and the VM
stopped, delete `$LYDIABOX_DATA_DIR/mnt/jfs/system/root-upper.img`, then
restart `lydiabox up` (it makes a new, empty overlay). Use
`LYDIABOX_VM_MEM_MB=4096` for Chrome and Blender.

The guest MCP server is at `http://127.0.0.1:8910/mcp` in the guest. In a
shell session:

```sh
mcp() { curl -s -m 600 -X POST http://127.0.0.1:8910/mcp -H 'Content-Type: application/json' \
  -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"$1\",\"arguments\":$2}}"; echo; }
```

| # | Check | Command | Expected |
| :--- | :--- | :--- | :--- |
| F1 | Wake | `api POST /api/v1/vm/wake` | `RUNNING`, `"driver":"chv"` |
| F2 | Desktop processes | `pgrep -af 'Xvfb\|startxfce4\|xfwm4\|xfce4-panel\|lydiabox-guest serve'`; `DISPLAY=:0 xdpyinfo \| grep dimensions` | `Xvfb :0 -screen 0 1920x1080x24`, `dbus-run-session -- /usr/bin/startxfce4`, `xfwm4`, `xfce4-panel`, `lydiabox-guest serve`; `1920x1080` |
| F3 | Tool list | `curl -s -X POST http://127.0.0.1:8910/mcp -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'` | 9 tools |
| F4 | Screenshot | `mcp computer_screenshot '{}'`; decode the base64 PNG | 1920x1080, the Xfce desktop with the panel, no error dialog |
| F5 | Chrome | `mcp desktop_launch_app '{"app":"chrome"}'` | `Launched chrome`; a `chromium` (arm64) or `chrome` (amd64) process with `--remote-debugging-port=9222` |
| F6 | CDP | `mcp browser_cdp_endpoint '{}'`; `curl http://127.0.0.1:9222/json/version` | `ws://127.0.0.1:9222/devtools/browser/...`; JSON with `webSocketDebuggerUrl`. The tool waits 30 s for a Chrome that it starts; call it again if Chrome starts more slowly |
| F7 | Blender | `mcp blender_run_script '{"code":"import bpy; print(bpy.app.version_string)"}'` | the version string in the output |
| F8 | Agent input | `mcp desktop_launch_app '{"app":"terminal"}'`; screenshot to find the window; `mcp computer_mouse_click '{"x":400,"y":300}'`; `mcp computer_keyboard '{"text":"echo LYDIA$((6*7))KVM","key_combo":"Return"}'`; screenshot | `LYDIA42KVM` in the terminal |
| F9 | Harness CLIs | `claude --version; codex --version; dsh --version; agy --version; opencode --version` | five version lines |

## 8. WebRTC from a browser

The browser must reach TCP 8080 and UDP `LYDIABOX_WEBRTC_UDP_PORT` (default
50000) of the KVM host. The guest advertises the address that the browser
used in the page URL as its ICE host candidate, and the driver forwards
the UDP port to the guest with an nftables DNAT rule. Lima port
forwarding does not reach the DNAT rule, so give the Lima VM an address
on the Mac: add `networks: [{vzNAT: true}]` to the Lima configuration
(the VM gets an address on `lima0`, for example `192.168.64.2`), and open
`http://192.168.64.2:8080` on the Mac.

| # | Check | How | Expected |
| :--- | :--- | :--- | :--- |
| W1 | DNAT rule | `sudo nft list table ip lydiabox_<tap>` while the VM runs | `iifname != "<tap>" udp dport 50000 dnat to 172.30.0.2` |
| W2 | Video | sign in, open the Desktop page; in the browser console: `v = document.querySelector('video'); [v.videoWidth, v.currentTime, v.getVideoPlaybackQuality().totalVideoFrames]` | `1920`, a time that increases, a frame count that increases |
| W3 | Media path | `sudo conntrack -L -p udp --dport 50000` on the KVM host | `src=<browser IP> dst=<host IP> ... dport=50000 src=172.30.0.2`, `[ASSURED]` |
| W4 | Takeover | click Takeover, click in a terminal window in the video, type `uname -n` and Enter; then `mcp computer_screenshot` | `lydiabox` in the terminal, each typed character once |
| W5 | Cleanup | `api POST /api/v1/vm/stop`; `nft list tables` | no `lydiabox_*` table, so no DNAT rule |

## 9. amd64 KVM host

The runs on 2026-11-10 used an arm64 host. The run on 2026-11-11 used an
amd64 Google Cloud `n2-standard-8` VM with nested virtualization; all
checks passed, with a wake in 3.7 s (results in
[conformance.md](conformance.md#integration-run-chv-on-kvm-amd64)). To
check an amd64 (x86_64) host, do these steps on a Linux amd64 host with
`/dev/kvm` (bare metal, or a cloud VM with nested virtualization):

1. Skip step 1. Do step 2.
2. Step 3: run `build-artifacts.sh` without `--virtiofsd` (on amd64,
   `lydiabox up` downloads the upstream virtiofsd), and use
   `dist/linux-amd64/`. Leave out the `runtime install-local virtiofsd`
   line. The kernel stage builds `bzImage` and installs
   `arch/x86/boot/compressed/vmlinux.bin` as `vmlinux`. The `desktop` stage
   installs Google Chrome (`google-chrome-stable`), not Chromium.
3. Do steps 4 and 5 (all 16 checks). Expected differences: check 2 shows
   `x86_64`; `serial.log` has the console output on `ttyS0`
   (`console=ttyS0`, the design text); `vm.info` shows the same
   `vm.create` payload as on arm64 except `cmdline`.
4. Do steps 7 and 8 (full image and WebRTC). In check F5, the browser is
   `google-chrome-stable`; `browser_cdp_endpoint` must give a
   `ws://127.0.0.1:9222/devtools/browser/...` URL.
5. With 4 or more vCPUs on bare metal, `POST /vm/wake` must finish well
   inside the 120 s boot limit. Record the time.
6. On a cloud VM behind 1:1 NAT, keep `lydiabox up` on `127.0.0.1:8080`
   and use an SSH tunnel for TCP 8080. The guest advertises the address of
   the host name in the page URL, so use a name that the browser maps to
   the tunnel and the host maps to its external address: for example
   `http://lydiabox-gcp.test:8080`, Chrome with
   `--host-resolver-rules="MAP lydiabox-gcp.test 127.0.0.1"`, and
   `<external IP> lydiabox-gcp.test` in `/etc/hosts` of the host. Allow
   UDP 50000 in the cloud firewall from the browser address only.

## 10. GPU passthrough (VFIO)

This needs an amd64 (or arm64 server) host with a PCI GPU that the host
does not use for its own display, and an IOMMU. The Lima VM has no PCI
device to pass through. The `vm.create` `devices` payload is covered by
`TestBuildConfigPayload` (`internal/vmm/chv/chv_test.go`); the address
format is checked by `TestValidate` (`internal/config/config_test.go`).

Host:

1. Enable VT-d or AMD-Vi in the firmware. Add `intel_iommu=on iommu=pt`
   (or `amd_iommu=on iommu=pt`) to the host kernel command line and
   reboot. Check: `dmesg | grep -i -e DMAR -e IOMMU` shows that the IOMMU
   is enabled, and `/sys/kernel/iommu_groups/` is not empty.
2. Find the GPU address and its IOMMU group:

   ```sh
   lspci -nnk | grep -A3 -i -e vga -e 3d
   GPU=0000:01:00.0
   ls /sys/bus/pci/devices/$GPU/iommu_group/devices/
   ```

   Every device in the group (usually the GPU and its HDMI audio function,
   for example `0000:01:00.1`) must be bound to `vfio-pci`. A group that
   also holds a PCI bridge with other devices cannot be passed through.
3. Bind each device of the group to `vfio-pci`:

   ```sh
   sudo modprobe vfio-pci
   for d in /sys/bus/pci/devices/$GPU/iommu_group/devices/*; do
     a=$(basename "$d")
     echo vfio-pci | sudo tee "$d/driver_override"
     [ -e "$d/driver" ] && echo "$a" | sudo tee "$d/driver/unbind"
     echo "$a" | sudo tee /sys/bus/pci/drivers_probe
   done
   lspci -nnk -s "$GPU"           # "Kernel driver in use: vfio-pci"
   ls -l /dev/vfio/               # a node for the group number
   ```

4. Start `lydiabox up` (step 4) with `LYDIABOX_GPU_PCI_ADDR=$GPU` added.
   A value that is not of the form `0000:01:00.0` stops the start with a
   configuration error.

Checks:

| # | Check | Command | Expected |
| :--- | :--- | :--- | :--- |
| G1 | Wake | `api POST /api/v1/vm/wake` | `RUNNING`. If it fails, `cloud-hypervisor.log` has the VFIO error (group not viable, device busy) |
| G2 | `vm.create` payload | `curl --unix-socket $DATA/run/vm/default/chv.sock http://localhost/api/v1/vm.info \| jq .config.devices` | `[{"path": "/sys/bus/pci/devices/0000:01:00.0", ...}]` |
| G3 | Device in the guest | in a shell session: `sudo apt-get install -y pciutils; lspci -nn` | the GPU with the same vendor and device IDs as on the host |
| G4 | Guest driver | install the vendor driver in the guest (it persists in the root overlay). The guest kernel is the Cloud Hypervisor kernel (`ch-6.12.8`), not an Ubuntu kernel, so a DKMS driver must be built against that kernel source (`make modules_prepare` in the kernel build tree), or `CONFIG_DRM_*` for the GPU must be added to `deploy/rootfs/kernel.config`. Then `nvidia-smi` (NVIDIA) or `ls /dev/dri` (AMD, Intel) | the GPU is listed |
| G5 | Cycles devices | `blender_run_script` with `import bpy; p = bpy.context.preferences.addons["cycles"].preferences; p.refresh_devices(); print([(d.name, d.type) for d in p.devices])` | a `CUDA`, `OPTIX`, `HIP`, or `ONEAPI` device besides `CPU`. The Ubuntu `blender` package can be built without CUDA and OptiX; if only `CPU` shows, install the official build from blender.org in the guest and run it again |
| G6 | Render | `blender_run_script` that sets `scene.cycles.device = "GPU"` and renders the default scene to `/workspace/gpu.png` | the render finishes faster than with `CPU` |
| G7 | Stop | `api POST /api/v1/vm/stop`; `lspci -nnk -s $GPU` on the host | `STOPPED`; the device is still bound to `vfio-pci` and can be used at the next wake |

Record the results in [conformance.md](conformance.md) (row "Optional
VFIO GPU device").
