#!/usr/bin/env sh
# Build the LydiaBox guest images with Docker and install them into the
# runtime images directory:
#
#   vmlinux             guest kernel
#   initramfs.cpio.gz   initramfs that assembles the OverlayFS root
#   rootfs.squashfs     read-only Ubuntu 24.04 desktop root filesystem
#   SHA256SUMS          checksums of the three files (sha256sum format)
#
# Usage: deploy/rootfs/build-artifacts.sh [--out DIR] [--arch amd64|arm64]
#                                         [--docker-image TAG] [--no-vm-images]
#
#   --out DIR           destination (default: $LYDIABOX_DATA_DIR/runtime/images,
#                       or ~/.lydiabox/runtime/images)
#   --arch ARCH         guest architecture (default: the Docker host architecture)
#   --docker-image TAG  also load the desktop image for the docker VM driver
#                       (LYDIABOX_DOCKER_IMAGE, default lydiabox/guest:latest)
#   --no-vm-images      skip vmlinux, initramfs.cpio.gz, and rootfs.squashfs
#                       (enough for the docker driver)
#
# `lydiabox runtime build-rootfs` runs this script from the repository root.
# The same directory layout can be published on a web server and used through
# LYDIABOX_ROOTFS_URL (`lydiabox runtime fetch-images`).
#
# Dockerfile contract (deploy/rootfs/Dockerfile): the build must define a
# stage named "artifacts" whose filesystem root holds exactly /vmlinux,
# /initramfs.cpio.gz, and /rootfs.squashfs, for example:
#
#   FROM scratch AS artifacts
#   COPY --from=kernel /out/vmlinux /vmlinux
#   COPY --from=initramfs /out/initramfs.cpio.gz /initramfs.cpio.gz
#   COPY --from=rootfs /out/rootfs.squashfs /rootfs.squashfs
#
# Requirements: Docker with BuildKit (docker buildx). Building a foreign
# architecture needs QEMU binfmt emulation (docker run --privileged --rm
# tonistiigi/binfmt --install all).
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH='' cd -- "$script_dir/../.." && pwd)
dockerfile="$script_dir/Dockerfile"

data_dir=${LYDIABOX_DATA_DIR:-"$HOME/.lydiabox"}
out_dir="$data_dir/runtime/images"
arch=""
docker_image=""
vm_images=1

die() {
	echo "build-artifacts: $*" >&2
	exit 1
}

while [ $# -gt 0 ]; do
	case "$1" in
	--out)
		[ $# -ge 2 ] || die "--out needs a directory"
		out_dir=$2
		shift 2
		;;
	--arch)
		[ $# -ge 2 ] || die "--arch needs amd64 or arm64"
		arch=$2
		shift 2
		;;
	--docker-image)
		[ $# -ge 2 ] || die "--docker-image needs an image tag"
		docker_image=$2
		shift 2
		;;
	--no-vm-images)
		vm_images=0
		shift
		;;
	-h | --help)
		sed -n '2,37p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		die "unknown argument: $1"
		;;
	esac
done

command -v docker >/dev/null 2>&1 || die "docker is not installed"
docker buildx version >/dev/null 2>&1 || die "docker buildx (BuildKit) is required"
[ -f "$dockerfile" ] || die "$dockerfile not found (the guest image is defined in Phase 2 of docs/design.md)"

if [ -z "$arch" ]; then
	case "$(docker version --format '{{.Server.Arch}}' 2>/dev/null || uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) die "cannot detect the Docker host architecture; pass --arch" ;;
	esac
fi
case "$arch" in
amd64 | arm64) ;;
*) die "--arch must be amd64 or arm64, got $arch" ;;
esac

if [ -n "$docker_image" ]; then
	echo "build-artifacts: building the linux/$arch desktop image $docker_image"
	docker buildx build \
		--platform "linux/$arch" \
		--file "$dockerfile" \
		--target desktop \
		--load \
		--tag "$docker_image" \
		"$repo_root"
fi
if [ "$vm_images" = 0 ]; then
	[ -n "$docker_image" ] || die "--no-vm-images without --docker-image builds nothing"
	exit 0
fi

mkdir -p "$out_dir"
stage=$(mktemp -d "$out_dir/.build-XXXXXX")
cleanup() {
	if [ -n "${stage:-}" ] && [ -d "$stage" ]; then
		find "$stage" -mindepth 1 -delete
		rmdir "$stage"
	fi
}
trap cleanup EXIT INT TERM

echo "build-artifacts: building linux/$arch guest images (this takes a while)"
docker buildx build \
	--platform "linux/$arch" \
	--file "$dockerfile" \
	--target artifacts \
	--output "type=local,dest=$stage" \
	"$repo_root"

for f in vmlinux initramfs.cpio.gz rootfs.squashfs; do
	[ -s "$stage/$f" ] || die "the artifacts stage did not produce /$f"
done

if command -v sha256sum >/dev/null 2>&1; then
	(cd "$stage" && sha256sum vmlinux initramfs.cpio.gz rootfs.squashfs >SHA256SUMS)
else
	(cd "$stage" && shasum -a 256 vmlinux initramfs.cpio.gz rootfs.squashfs >SHA256SUMS)
fi

for f in vmlinux initramfs.cpio.gz rootfs.squashfs SHA256SUMS; do
	mv -f "$stage/$f" "$out_dir/$f"
done
echo "build-artifacts: installed guest images in $out_dir"
