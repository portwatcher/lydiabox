#!/usr/bin/env sh
# Build the LydiaBox image for InstaBox (linux/amd64) and print the
# import command. The import needs an InstaBox account and a registry
# that InstaBox can pull from, so this script never runs it.
#
# Usage: deploy/instabox/build.sh [--tag IMAGE] [--base IMAGE] [--slim]
#                                 [--distro IMAGE] [--no-base] [--push]
#                                 [--name SNAPSHOT]
#
#   --tag IMAGE      image to build (default lydiabox/instabox:latest; use a
#                    registry reference such as ghcr.io/OWNER/lydiabox-instabox:TAG
#                    with --push)
#   --base IMAGE     base image tag (default lydiabox/guest:instabox-base,
#                    or lydiabox/guest:instabox-base-slim with --slim)
#   --slim           use the desktop-slim stage as the base (no Chrome,
#                    Blender, Node.js, or harness CLIs; for tests)
#   --distro IMAGE   distribution of the base (DESKTOP_IMAGE of
#                    deploy/rootfs/Dockerfile; default debian:trixie-slim, the
#                    release of the InstaBox machine template, so that the
#                    image's programs match the machine's shared libraries)
#   --no-base        reuse an existing base image instead of building it
#   --push           push the image after the build (docker login first)
#   --name SNAPSHOT  snapshot name in the printed command (default lydiabox-desktop)
#
# Requirements: Docker with BuildKit (docker buildx). On an arm64 host the
# amd64 build runs under emulation (Docker Desktop, or QEMU binfmt:
# docker run --privileged --rm tonistiigi/binfmt --install amd64).
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH='' cd -- "$script_dir/../.." && pwd)
tag=lydiabox/instabox:latest
base=""
stage=desktop
distro=debian:trixie-slim
build_base=1
push=0
name=lydiabox-desktop

die() {
	echo "instabox build: $*" >&2
	exit 1
}

while [ $# -gt 0 ]; do
	case "$1" in
	--tag)
		[ $# -ge 2 ] || die "--tag needs an image reference"
		tag=$2
		shift 2
		;;
	--base)
		[ $# -ge 2 ] || die "--base needs an image reference"
		base=$2
		shift 2
		;;
	--slim)
		stage=desktop-slim
		shift
		;;
	--distro)
		[ $# -ge 2 ] || die "--distro needs an image reference"
		distro=$2
		shift 2
		;;
	--no-base)
		build_base=0
		shift
		;;
	--push)
		push=1
		shift
		;;
	--name)
		[ $# -ge 2 ] || die "--name needs a snapshot name"
		name=$2
		shift 2
		;;
	-h | --help)
		sed -n '2,27p' "$0"
		exit 0
		;;
	*) die "unknown argument $1" ;;
	esac
done

case "$name" in
'' | *[!a-z0-9-]* | -*) die "snapshot name $name must be 1-63 of a-z, 0-9, - (not first)" ;;
esac
if [ -z "$base" ]; then
	base=lydiabox/guest:instabox-base
	[ "$stage" = desktop ] || base=lydiabox/guest:instabox-base-slim
fi

command -v docker >/dev/null 2>&1 || die "docker is required"
docker buildx version >/dev/null 2>&1 || die "docker buildx (BuildKit) is required"
cd "$repo_root"

if [ "$build_base" -eq 1 ]; then
	echo "instabox build: base $base (deploy/rootfs/Dockerfile, target $stage, $distro, linux/amd64)"
	docker buildx build --platform linux/amd64 \
		-f deploy/rootfs/Dockerfile --target "$stage" --build-arg "DESKTOP_IMAGE=$distro" \
		-t "$base" --load .
else
	docker image inspect "$base" >/dev/null 2>&1 || die "base image $base not found; drop --no-base"
fi
arch=$(docker image inspect --format '{{.Architecture}}' "$base")
[ "$arch" = amd64 ] || die "base image $base is $arch; InstaBox imports only linux/amd64"

# A plain single-platform image: no provenance or SBOM attestations in an
# index for the importer to skip.
echo "instabox build: image $tag"
docker buildx build --platform linux/amd64 \
	-f deploy/instabox/Dockerfile --build-arg "BASE_IMAGE=$base" \
	--provenance=false --sbom=false \
	-t "$tag" --load .

if [ "$push" -eq 1 ]; then
	docker push "$tag"
fi

# A pushed image is imported by its registry digest; a local one has
# none that InstaBox could pull.
digest=""
if [ "$push" -eq 1 ]; then
	digest=$(docker image inspect --format '{{index .RepoDigests 0}}' "$tag" 2>/dev/null || true)
fi
cat <<EOF

Built $tag (linux/amd64).
Container test:  deploy/instabox/test.sh --image $tag
EOF
if [ "$push" -eq 0 ]; then
	cat <<EOF
InstaBox pulls the image from a registry. Tag it with a registry
reference and push it first, for example:
  docker tag $tag ghcr.io/OWNER/lydiabox-instabox:TAG
  docker push ghcr.io/OWNER/lydiabox-instabox:TAG
EOF
fi
cat <<EOF
Import (needs INSTABOX_API_KEY; takes a few minutes; private registry:
add --registry-username U with INSTABOX_REGISTRY_PASSWORD set):
  instabox snapshot import ${digest:-$tag} --name $name
EOF
