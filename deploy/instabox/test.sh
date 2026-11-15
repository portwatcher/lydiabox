#!/usr/bin/env sh
# Container test of the LydiaBox InstaBox image: runs it with Docker as
# linux/amd64 (emulated on an arm64 host), with a volume as /data, and
# checks that X11, the Xfce desktop, and lydiabox-guest (as UID 1000, with
# HOME /home/agent, a link to /data/home) start, that the guest waits for
# its settings file, that a host link reaches it
# (internal/instabox/imagetest: ping, a desktop screenshot, a PTY, and the
# broker bridge), and that it starts again from the kept settings after a
# restart. Exits 0 with a SKIP line when Docker is not available.
#
# Usage: deploy/instabox/test.sh [--image IMAGE] [--build [build.sh flags]]
#
#   --image IMAGE  image to test (default lydiabox/instabox:latest)
#   --build        build it first with deploy/instabox/build.sh; the
#                  remaining arguments go to build.sh (for example --slim)
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
image=lydiabox/instabox:latest
build=0
while [ $# -gt 0 ]; do
	case "$1" in
	--image)
		[ $# -ge 2 ] || {
			echo "--image needs an image" >&2
			exit 2
		}
		image=$2
		shift 2
		;;
	--build)
		build=1
		shift
		break
		;;
	*)
		echo "unknown argument $1" >&2
		exit 2
		;;
	esac
done

if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
	echo "SKIP: Docker is not available; the InstaBox image test did not run"
	exit 0
fi
if [ "$build" -eq 1 ]; then
	"$script_dir/build.sh" --tag "$image" "$@"
fi
if ! docker image inspect "$image" >/dev/null 2>&1; then
	echo "FAIL: image $image not found; build it with deploy/instabox/build.sh or pass --build" >&2
	exit 1
fi
arch=$(docker image inspect --format '{{.Architecture}}' "$image")
[ "$arch" = amd64 ] || {
	echo "FAIL: $image is $arch, not amd64" >&2
	exit 1
}

name=lbx-instabox-test-$$
volume=$name-data
cleanup() {
	docker rm -f "$name" >/dev/null 2>&1 || true
	docker volume rm "$volume" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

fail() {
	echo "FAIL: $*" >&2
	echo "--- container log" >&2
	docker logs "$name" 2>&1 | tail -40 >&2 || true
	echo "--- supervisor logs" >&2
	docker exec "$name" sh -c 'tail -n 20 /var/log/supervisor/*.log' >&2 2>&1 || true
	exit 1
}

# in_ctr runs a shell command in the container, as root or as the user
# in $as_user (root cannot read another user's /proc/<pid>/environ
# without CAP_SYS_PTRACE).
as_user=0
in_ctr() { docker exec -u "$as_user" "$name" sh -c "$1"; }

# wait_for CHECK DESCRIPTION [SECONDS]
wait_for() {
	limit=${3:-180}
	i=0
	until in_ctr "$1" >/dev/null 2>&1; do
		i=$((i + 2))
		[ "$i" -lt "$limit" ] || fail "$2 within ${limit}s"
		sleep 2
	done
	echo "ok: $2"
}

docker volume create "$volume" >/dev/null
docker run -d --name "$name" --platform linux/amd64 --shm-size 1g \
	-p 127.0.0.1::9000 -v "$volume:/data" "$image" >/dev/null

wait_for 'test -S /tmp/.X11-unix/X0' "Xvfb socket :0"
wait_for 'su agent -s /bin/sh -c "DISPLAY=:0 xdpyinfo" || su agent -s /bin/sh -c "DISPLAY=:0 xprop -root"' "X11 display :0 answers"
wait_for 'pgrep -u 1000 -x xfwm4 && pgrep -u 1000 -f "xfce4-sessio[n]"' "Xfce session and window manager as UID 1000"
wait_for 'pgrep -u 1000 -f "lydiabox-instabox-gues[t]"' "guest wrapper runs as UID 1000"
sleep 3
docker logs "$name" 2>&1 | grep -q "waiting for /data/.lydiabox/guest.env" || fail "the guest wrapper does not wait for guest.env"
if in_ctr 'pgrep -f "lydiabox-guest [s]erve"' >/dev/null 2>&1; then
	fail "lydiabox-guest started before guest.env existed"
fi
echo "ok: lydiabox-guest waits for guest.env"

home=$(in_ctr 'getent passwd agent | cut -d: -f6')
[ "$home" = /home/agent ] || fail "agent home is $home, want /home/agent"
[ "$(in_ctr 'readlink /home/agent')" = /data/home ] || fail "/home/agent does not link to /data/home"
[ "$(in_ctr 'readlink /workspace')" = /data/workspace ] || fail "/workspace does not link to /data/workspace"
owner=$(in_ctr 'stat -c %u:%g /data/home /data/workspace | sort -u')
[ "$owner" = 1000:1000 ] || fail "/data/home and /data/workspace owner $owner, want 1000:1000"
echo "ok: /home/agent -> /data/home, /workspace -> /data/workspace, owner UID 1000"

# The driver writes the settings file as UID 1000 (the InstaBox files
# API): the host link listener on port 9000 and its secret.
secret=lbx-test-$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
docker exec -u 1000 -e "SECRET=$secret" "$name" sh -c 'umask 077; cat >/data/.lydiabox/guest.env <<EOF
LYDIABOX_GUEST_LISTEN=ws://0.0.0.0:9000/
LYDIABOX_LINK_SECRET=$SECRET
LYDIABOX_WEBRTC_UDP_PORT=0
NOT_LYDIABOX=ignored
LYDIABOX_LOG_LEVEL=debug
EOF'
check_guest() {
	wait_for 'pgrep -u 1000 -f "lydiabox-guest [s]erve" && test -S /run/lydiabox/host-9001.sock' "lydiabox-guest serves the host link as UID 1000 ($1)" 90
	# shellcheck disable=SC2016 # expanded in the container
	env_check='pid=$(pgrep -u 1000 -f "lydiabox-guest [s]erve" | head -n 1); env=$(tr "\0" "\n" </proc/$pid/environ);'
	# shellcheck disable=SC2016
	env_check="$env_check"' echo "$env" | grep -qx HOME=/home/agent && echo "$env" | grep -qx LYDIABOX_LOG_LEVEL=debug'
	# shellcheck disable=SC2016
	env_check="$env_check"' && echo "$env" | grep -qx LYDIABOX_GUEST_WORKSPACE_ROOT=/data/workspace && ! echo "$env" | grep -q NOT_LYDIABOX'
	as_user=1000
	wait_for "$env_check" "guest environment from guest.env ($1)" 30
	as_user=0
	probe "$1"
}

# probe connects to the guest over the real host link with the Go test in
# internal/instabox/imagetest (needs Go and this checkout).
repo_root=$(CDPATH='' cd -- "$script_dir/../.." && pwd)
probe() {
	if ! command -v go >/dev/null 2>&1 || [ ! -f "$repo_root/go.mod" ]; then
		echo "SKIP: link probe ($1): Go or the repository is not available"
		return
	fi
	hostport=$(docker port "$name" 9000/tcp | head -n 1)
	(cd "$repo_root" && LYDIABOX_IMAGE_LINK_URL="ws://$hostport/" LYDIABOX_IMAGE_LINK_SECRET="$secret" \
		CGO_ENABLED=0 go test ./internal/instabox/imagetest -run TestImageLink -count=1 -v) ||
		fail "link probe ($1)"
	echo "ok: ping, desktop screenshot, PTY, and broker bridge over the host link ($1)"
}

check_guest "first start"
docker logs "$name" 2>&1 | grep -q "lydiabox-guest serving" || fail "no 'lydiabox-guest serving' line in the container log"
echo "ok: guest log line"

# Stop and start: the persistent disk keeps guest.env, so the guest starts
# without the driver writing it again.
docker restart "$name" >/dev/null
wait_for 'test -S /tmp/.X11-unix/X0' "Xvfb after restart"
wait_for 'pgrep -u 1000 -x xfwm4' "Xfce after restart"
check_guest "after restart"

echo "PASS: $image"
