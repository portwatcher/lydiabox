#!/bin/sh
# System upgrade of an InstaBox machine (docs/instabox.md, "System
# upgrade"). The instabox driver runs this script as root in an existing
# machine, from a root-owned temporary directory that holds the system
# bundle (deploy/instabox/system.go):
#
#   sh system-upgrade.sh BUNDLE_DIR
#
# InstaBox has no operation that replaces the root disk and keeps /data,
# so a machine keeps the system files of its snapshot. This script brings
# them to the state that the current core expects.
#
# Rules for each step:
# - Check first. Change a file only when it is not correct. A second run
#   changes nothing.
# - Replace a file atomically: write NAME.lydiabox-new, then mv -f.
# - Never restart supervisord, the desktop, or the guest daemon. A new
#   entry point or supervisord configuration takes effect at the next start
#   of the machine.
# - Change nothing below /data. (Step 2 reads the guest binary there.)
# - On an error, stop with exit status not 0 and a clear message. Then the
#   driver does not write the marker, and a later process tries again.
#
# Each change prints one line "changed: ...". The driver logs these lines.
set -eu
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export LC_ALL=C

die() {
	echo "ERROR: $*" >&2
	exit 1
}
changed() {
	echo "changed: $*"
}

[ $# -eq 1 ] || die "usage: system-upgrade.sh BUNDLE_DIR"
bundle=$1
[ "$(id -u)" -eq 0 ] || die "run this script as root"
[ -r "$bundle/files.list" ] || die "$bundle/files.list is missing"

# put SOURCE FILE MODE: FILE gets the content of SOURCE and MODE (octal,
# for example 0644), owner root.
put() {
	if [ -f "$2" ] && [ ! -L "$2" ] && cmp -s "$1" "$2" &&
		[ "$(stat -c %a "$2")" = "${3#0}" ] && [ "$(stat -c %u "$2")" = 0 ]; then
		return 0
	fi
	mkdir -p "$(dirname "$2")"
	rm -f "$2.lydiabox-new"
	cp "$1" "$2.lydiabox-new"
	chown 0:0 "$2.lydiabox-new"
	chmod "$3" "$2.lydiabox-new"
	mv -f "$2.lydiabox-new" "$2"
	changed "$2"
}

# link TARGET NAME: NAME is a symbolic link to TARGET.
link() {
	if [ -L "$2" ] && [ "$(readlink "$2")" = "$1" ]; then
		return 0
	fi
	rm -f "$2.lydiabox-new"
	ln -s "$1" "$2.lydiabox-new"
	mv -Tf "$2.lydiabox-new" "$2"
	changed "$2 -> $1"
}

# Step 1: the image's own files (deploy/instabox/Dockerfile). The entry
# point, the guest and Xfce wrappers, and the supervisord configuration
# take effect at the next start of the machine. The harness and panel
# settings in /usr/local/share/lydiabox/etc go to /etc at that start too
# (the entry point copies them).
while read -r mode dest name; do
	case "$dest" in
	/data | /data/*) die "files.list: $dest is below /data" ;;
	/*) ;;
	*) die "files.list: $dest is not an absolute path" ;;
	esac
	case "$name" in
	*/* | "" | .*) die "files.list: bad file name '$name'" ;;
	esac
	[ -f "$bundle/files/$name" ] || die "the bundle has no files/$name"
	put "$bundle/files/$name" "$dest" "$mode"
done <"$bundle/files.list"

# Step 2: the guest binary of the image path. The guest binary upgrade
# puts the host's guest on /data, and the image's wrapper runs it. lbx,
# git-credential-lydiabox, and the git credential helper run the image
# path, so it gets the same binary. The rename keeps the inode of a
# running daemon.
guest=/usr/local/bin/lydiabox-guest
override=/data/.lydiabox/bin/lydiabox-guest
if [ -f "$override" ] && [ ! -L "$override" ] && [ -x "$override" ]; then
	put "$override" "$guest" 0755
fi

# Step 3: the links to the guest binary (deploy/rootfs/setup-guest.sh)
# and the git credential helper.
link "$guest" /usr/local/bin/lbx
link "$guest" /usr/local/bin/git-credential-lydiabox
helper="$guest git-cred"
if command -v git >/dev/null 2>&1 &&
	[ "$(git config --system --get-all credential.helper 2>/dev/null || true)" != "$helper" ]; then
	git config --system --replace-all credential.helper "$helper"
	changed "git config --system credential.helper"
fi

# Step 4: Debian packages that the image has (deploy/rootfs/Dockerfile)
# and an older snapshot does not. Only the missing ones are installed.
# - bubblewrap: Codex's sandbox (bwrap).
packages="bubblewrap"
missing=""
for p in $packages; do
	if ! dpkg-query -W -f '${Status}\n' "$p" 2>/dev/null | grep -q ' ok installed$'; then
		missing="$missing $p"
	fi
done
if [ -n "$missing" ]; then
	export DEBIAN_FRONTEND=noninteractive
	apt="apt-get -q -o DPkg::Lock::Timeout=120"
	# shellcheck disable=SC2086 # word splitting is intended
	$apt update || die "apt-get update failed; the packages$missing are not installed"
	# shellcheck disable=SC2086 # word splitting is intended
	$apt install -y --no-install-recommends $missing || die "apt-get install$missing failed"
	changed "installed the packages$missing"
fi

echo "the system files are current"
