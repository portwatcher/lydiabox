# syntax=docker/dockerfile:1
# Builds virtiofsd from source as a static musl binary for the target
# platform. Upstream publishes a static x86_64 build only, so LydiaBox builds
# the linux/arm64 binary itself (deploy/virtiofsd/build.sh, `make virtiofsd`).
#
#   docker buildx build --platform linux/arm64 --target out \
#     --output type=local,dest=OUT -f deploy/virtiofsd/Dockerfile deploy/virtiofsd
#
# The flags follow the upstream musl CI job.
ARG RUST_IMAGE=rust:1.88-alpine3.22

FROM ${RUST_IMAGE} AS build
ARG VIRTIOFSD_VERSION=v1.14.0
RUN apk add --no-cache binutils git musl-dev libcap-ng-dev libcap-ng-static libseccomp-dev libseccomp-static
RUN git clone --depth 1 --branch "${VIRTIOFSD_VERSION}" https://gitlab.com/virtio-fs/virtiofsd.git /src
WORKDIR /src
ENV RUSTFLAGS="-C target-feature=+crt-static -C link-self-contained=yes" \
	LIBSECCOMP_LINK_TYPE=static LIBSECCOMP_LIB_PATH=/usr/lib \
	LIBCAPNG_LINK_TYPE=static LIBCAPNG_LIB_PATH=/usr/lib
RUN target="$(uname -m)-unknown-linux-musl" \
	&& cargo build --locked --release --target "$target" \
	&& install -m 0755 "target/$target/release/virtiofsd" /virtiofsd \
	&& strip /virtiofsd \
	&& if readelf -l /virtiofsd | grep -q "program interpreter"; then echo "virtiofsd is not static" >&2; exit 1; fi \
	&& /virtiofsd --version

FROM scratch AS out
COPY --from=build /virtiofsd /virtiofsd
