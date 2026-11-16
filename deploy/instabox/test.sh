#!/usr/bin/env sh
# Container test of the LydiaBox InstaBox image: runs it with Docker as
# linux/amd64 (emulated on an arm64 host), with a volume as /data, and
# checks that X11, the Xfce desktop (with no dialog and no plugin outside
# the panel's process), and lydiabox-guest (as UID 1000, with HOME
# /home/agent, a link to /data/lydiabox/home) start, that the guest waits
# for its settings file, that a host link reaches it
# (internal/instabox/imagetest: ping, a desktop screenshot, a PTY, the
# broker bridge, the PTY environment, and a terminal that desktop.launch
# starts in /home/agent with PWD=/home/agent, as the Xfce panel does),
# that the harness CLIs load no InstaBox MCP server, and that it starts
# again from the kept settings after a restart. Exits 0 with a SKIP line when Docker is not available.
#
# Scenarios:
#   plain    the image as it is (UID 1000 is "agent", from the base image)
#   machine  the image as an InstaBox import makes it
#            (deploy/instabox/machine-model): no user "agent", UID 1000
#            named "alice" with the home /data/home that has InstaBox's own
#            files, a dbus-run-session that fails, the user manager's
#            session bus (HOME /data/home), the template's Codex settings
#            and harness links, systemd's and supplier variables in the
#            unit, and a socket as the standard output of the entry point
#            (the journal)
#   migrate  the machine scenario on a disk of the old layout (core
#            802e85c and earlier: /home/agent -> /data/home): the start
#            moves the link one time and leaves /data/home unchanged
#   precond  the entry point fails with a clear error when UID 1000 or
#            /data is missing
#
# Usage: deploy/instabox/test.sh [--image IMAGE] [--scenario NAME]
#                                [--build [build.sh flags]]
#
#   --image IMAGE    image to test (default lydiabox/instabox:latest)
#   --scenario NAME  plain, machine, migrate, precond, or all (default all)
#   --build          build it first with deploy/instabox/build.sh; the
#                    remaining arguments go to build.sh (for example --slim)
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH='' cd -- "$script_dir/../.." && pwd)
image=lydiabox/instabox:latest
scenario=all
build=0
while [ $# -gt 0 ]; do
	case "$1" in
	--image | --scenario)
		[ $# -ge 2 ] || {
			echo "$1 needs a value" >&2
			exit 2
		}
		if [ "$1" = --image ]; then image=$2; else scenario=$2; fi
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
case "$scenario" in
plain | machine | migrate | precond | all) ;;
*)
	echo "unknown scenario $scenario" >&2
	exit 2
	;;
esac

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

prefix=lbx-instabox-test-$$
model=lbx-instabox-model-$$:test
name=$prefix
volume=$name-data
cleanup() {
	for n in "$prefix-plain" "$prefix-machine" "$prefix-migrate"; do
		docker rm -f "$n" >/dev/null 2>&1 || true
		docker volume rm "$n-data" >/dev/null 2>&1 || true
	done
	docker image rm -f "$model" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

fail() {
	echo "FAIL ($current): $*" >&2
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
		[ "$(docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null)" = true ] ||
			fail "the container stopped while waiting for: $2"
		i=$((i + 2))
		[ "$i" -lt "$limit" ] || fail "$2 within ${limit}s"
		sleep 2
	done
	echo "ok ($current): $2"
}

guest_log=/var/log/supervisor/lydiabox-guest.log

# probe connects to the guest over the real host link with the Go test in
# internal/instabox/imagetest (needs Go and this checkout).
probe() {
	if ! command -v go >/dev/null 2>&1 || [ ! -f "$repo_root/go.mod" ]; then
		echo "SKIP: link probe ($current, $1): Go or the repository is not available"
		return
	fi
	hostport=$(docker port "$name" 9000/tcp | head -n 1)
	(cd "$repo_root" && LYDIABOX_IMAGE_LINK_URL="ws://$hostport/" LYDIABOX_IMAGE_LINK_SECRET="$secret" \
		CGO_ENABLED=0 go test ./internal/instabox/imagetest -run TestImageLink -count=1 -v) ||
		fail "link probe ($1)"
	echo "ok ($current): ping, desktop screenshot, PTY, and broker bridge over the host link ($1)"
}

# check_guest WHEN: the guest daemon runs as UID 1000 with the settings of
# guest.env and the user name $user, and the link probe passes.
check_guest() {
	wait_for 'pgrep -u 1000 -f "lydiabox-guest [s]erve" && test -S /run/lydiabox/host-9001.sock' "lydiabox-guest serves the host link as UID 1000 ($1)" 90
	# shellcheck disable=SC2016 # expanded in the container
	env_check='pid=$(pgrep -u 1000 -f "lydiabox-guest [s]erve" | head -n 1); env=$(tr "\0" "\n" </proc/$pid/environ);'
	# shellcheck disable=SC2016
	env_check="$env_check"' echo "$env" | grep -qx HOME=/home/agent && echo "$env" | grep -qx LYDIABOX_LOG_LEVEL=debug'
	# shellcheck disable=SC2016
	env_check="$env_check"' && echo "$env" | grep -qx LYDIABOX_GUEST_WORKSPACE_ROOT=/data/workspace && ! echo "$env" | grep -q NOT_LYDIABOX'
	# shellcheck disable=SC2016
	env_check="$env_check"' && echo "$env" | grep -qx LYDIABOX_GUEST_HOME_ROOT=/data/lydiabox/home'
	# The unit's variables (systemd's and the model's supplier variables)
	# do not reach the programs.
	# shellcheck disable=SC2016
	env_check="$env_check"' && ! echo "$env" | grep -qE "^(INVOCATION_ID|JOURNAL_STREAM|SYSTEMD_EXEC_PID|ANTHROPIC_API_KEY|INSTABOX_API_KEY|CLAUDE_CONFIG_DIR|CODEX_HOME|XDG_CONFIG_HOME|DEBIAN_FRONTEND)="'
	# shellcheck disable=SC2016
	env_check="$env_check"' && echo "$env" | grep -qx USER='"$user"' && echo "$env" | grep -qx XDG_RUNTIME_DIR=/run/user/1000'
	as_user=1000
	wait_for "$env_check" "guest environment from guest.env, USER=$user ($1)" 30
	as_user=0
	probe "$1"
}

# shellcheck source=deploy/instabox/test-checks.sh
. "$script_dir/test-checks.sh"

# run_desktop SCENARIO IMAGE USER: the full check of one image.
run_desktop() {
	current=$1
	user=$3
	name=$prefix-$1
	volume=$name-data
	docker volume create "$volume" >/dev/null
	model_env=""
	[ "$current" != migrate ] || model_env=INSTABOX_MODEL_OLD_LAYOUT=1
	docker run -d --name "$name" --platform linux/amd64 --shm-size 1g \
		-p 127.0.0.1::9000 -v "$volume:/data" -e "${model_env:-INSTABOX_MODEL_OLD_LAYOUT=0}" "$2" >/dev/null

	wait_for 'test -S /tmp/.X11-unix/X0' "Xvfb socket :0"
	as_user=1000
	wait_for 'DISPLAY=:0 xdpyinfo || DISPLAY=:0 xprop -root' "X11 display :0 answers UID 1000"
	as_user=0
	wait_for 'pgrep -u 1000 -x xfwm4 && pgrep -u 1000 -f "xfce4-sessio[n]"' "Xfce session and window manager as UID 1000"
	wait_for 'pgrep -u 1000 -f "lydiabox-instabox-gues[t]"' "guest wrapper runs as UID 1000"
	sleep 3
	in_ctr "grep -q 'waiting for /data/.lydiabox/guest.env' $guest_log" || fail "the guest wrapper does not wait for guest.env"
	if in_ctr 'pgrep -f "lydiabox-guest [s]erve"' >/dev/null 2>&1; then
		fail "lydiabox-guest started before guest.env existed"
	fi
	echo "ok ($current): lydiabox-guest waits for guest.env"

	[ "$(in_ctr 'getent passwd 1000 | cut -d: -f1')" = "$user" ] || fail "UID 1000 is not $user"
	check_layout
	echo "ok ($current): UID 1000 is $user; /home/agent -> /data/lydiabox/home, /workspace -> /data/workspace, owner UID 1000"
	check_desktop
	check_harnesses
	if [ "$current" != plain ]; then
		in_ctr '! getent passwd agent' || fail "the machine model has a user agent"
	fi
	check_session_bus

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
	if [ "$current" = migrate ]; then
		# The driver of core 802e85c and earlier also wrote the old root.
		docker exec -u 1000 "$name" sh -c 'echo LYDIABOX_GUEST_HOME_ROOT=/data/home >>/data/.lydiabox/guest.env'
	fi
	check_guest "first start"
	in_ctr "grep -q 'lydiabox-guest serving' $guest_log" || fail "no 'lydiabox-guest serving' line in $guest_log"
	echo "ok ($current): guest log line"
	if [ "$current" = migrate ]; then
		in_ctr "grep -q 'ignoring LYDIABOX_GUEST_HOME_ROOT' $guest_log" || fail "the wrapper did not ignore the old file root in guest.env"
		echo "ok ($current): the wrapper ignores the old file root in guest.env"
	fi
	# The driver's restart after changed settings (sudo through exec).
	in_ctr 'supervisorctl -c /etc/lydiabox/instabox-supervisord.conf restart lydiabox-guest' >/dev/null ||
		fail "supervisorctl restart lydiabox-guest"
	echo "ok ($current): supervisorctl restarts the guest daemon"

	# Stop and start: the persistent disk keeps guest.env, so the guest
	# starts without the driver writing it again.
	docker restart "$name" >/dev/null
	wait_for 'test -S /tmp/.X11-unix/X0' "Xvfb after restart"
	wait_for 'pgrep -u 1000 -x xfwm4' "Xfce after restart"
	check_guest "after restart"
	check_layout
	check_desktop
	echo "ok ($current): layout and desktop after restart"
	docker rm -f "$name" >/dev/null
	docker volume rm "$volume" >/dev/null
}

build_model() {
	[ -n "${model_built:-}" ] && return
	docker build -q --platform linux/amd64 --build-arg "IMAGE=$image" -t "$model" "$script_dir/machine-model" >/dev/null
	model_built=1
}

# expect_entry_error DESCRIPTION SETUP MESSAGE: the entry point exits
# non-zero with MESSAGE after SETUP (a shell command run as root first).
expect_entry_error() {
	set +e
	out=$(docker run --rm --platform linux/amd64 --entrypoint /bin/sh "$model" -c "$2; exec /usr/local/sbin/lydiabox-instabox-entry" 2>&1)
	rc=$?
	set -e
	if [ "$rc" -eq 0 ] || ! echo "$out" | grep -q "$3"; then
		echo "$out" | tail -n 5 >&2
		echo "FAIL (precond): $1: exit $rc, want non-zero and '$3'" >&2
		exit 1
	fi
	echo "ok (precond): $1"
}

if [ "$scenario" = plain ] || [ "$scenario" = all ]; then
	run_desktop plain "$image" agent
fi
if [ "$scenario" = machine ] || [ "$scenario" = all ]; then
	build_model
	run_desktop machine "$model" alice
fi
if [ "$scenario" = migrate ] || [ "$scenario" = all ]; then
	build_model
	run_desktop migrate "$model" alice
fi
if [ "$scenario" = precond ] || [ "$scenario" = all ]; then
	build_model
	current=precond
	expect_entry_error "no user with UID 1000" "mkdir -p /data && userdel alice" "ERROR: no user with UID 1000"
	expect_entry_error "no /data" "rm -rf /data" "ERROR: /data does not exist"
	expect_entry_error "/home/agent is a directory" "mkdir -p /data && rm /home/agent && mkdir /home/agent" "ERROR: /home/agent exists and is not a link"
fi

echo "PASS: $image ($scenario)"
