#!/usr/bin/env sh
# Build virtiofsd v1.14.0 from source with Docker as a static musl binary.
# Upstream publishes a static linux/amd64 build only; LydiaBox downloads that
# one and builds linux/arm64 with this script (`make virtiofsd`).
#
# Usage: deploy/virtiofsd/build.sh [--arch amd64|arm64] [--out FILE]
#                                  [--install DIR]
#
#   --arch ARCH     target architecture (default: the Docker host architecture)
#   --out FILE      output file (default: dist/virtiofsd-linux-ARCH)
#   --install DIR   also copy the binary to DIR/virtiofsd (for example
#                   $LYDIABOX_DATA_DIR/runtime/bin)
#
# Requirements: Docker with BuildKit (docker buildx). Building a foreign
# architecture needs QEMU binfmt emulation and is slow.
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH='' cd -- "$script_dir/../.." && pwd)
arch=""
out=""
install_dir=""

die() {
	echo "virtiofsd-build: $*" >&2
	exit 1
}

while [ $# -gt 0 ]; do
	case "$1" in
	--arch)
		[ $# -ge 2 ] || die "--arch needs amd64 or arm64"
		arch=$2
		shift 2
		;;
	--out)
		[ $# -ge 2 ] || die "--out needs a file"
		out=$2
		shift 2
		;;
	--install)
		[ $# -ge 2 ] || die "--install needs a directory"
		install_dir=$2
		shift 2
		;;
	-h | --help)
		sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		die "unknown argument: $1"
		;;
	esac
done

command -v docker >/dev/null 2>&1 || die "docker is not installed"
docker buildx version >/dev/null 2>&1 || die "docker buildx (BuildKit) is required"
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
[ -n "$out" ] || out="$repo_root/dist/virtiofsd-linux-$arch"

out_dir=$(dirname -- "$out")
mkdir -p "$out_dir"
stage=$(mktemp -d "$out_dir/.virtiofsd-XXXXXX")
cleanup() {
	if [ -n "${stage:-}" ] && [ -d "$stage" ]; then
		find "$stage" -mindepth 1 -delete
		rmdir "$stage"
	fi
}
trap cleanup EXIT INT TERM

echo "virtiofsd-build: building virtiofsd for linux/$arch (several minutes)"
docker buildx build \
	--platform "linux/$arch" \
	--file "$script_dir/Dockerfile" \
	--target out \
	--output "type=local,dest=$stage" \
	"$script_dir"
[ -s "$stage/virtiofsd" ] || die "the build did not produce virtiofsd"
chmod 0755 "$stage/virtiofsd"
mv -f "$stage/virtiofsd" "$out"
echo "virtiofsd-build: wrote $out"

if [ -n "$install_dir" ]; then
	mkdir -p "$install_dir"
	cp "$out" "$install_dir/.virtiofsd.tmp"
	chmod 0755 "$install_dir/.virtiofsd.tmp"
	mv -f "$install_dir/.virtiofsd.tmp" "$install_dir/virtiofsd"
	echo "virtiofsd-build: installed $install_dir/virtiofsd"
fi
