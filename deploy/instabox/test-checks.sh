# shellcheck shell=sh disable=SC2154,SC2034 # current, name, as_user: test.sh
# Checks of deploy/instabox/test.sh for one running container ($name,
# scenario $current), sourced by it: the disk layout, the desktop, and the
# harness configuration. They use in_ctr, as_user, wait_for, and fail of
# test.sh.

# check_layout: /home/agent links to LydiaBox's home, /workspace to the
# workspace; both belong to UID 1000 and the seed is in the home. On the
# machine model, InstaBox's /data/home is exactly as the template made it
# and none of its files is in LydiaBox's home; the old layout moved one
# time, a new disk never.
check_layout() {
	link=$(in_ctr 'readlink /home/agent')
	[ "$link" = /data/lydiabox/home ] || fail "/home/agent links to $link, want /data/lydiabox/home"
	[ "$(in_ctr 'readlink /workspace')" = /data/workspace ] || fail "/workspace does not link to /data/workspace"
	owner=$(in_ctr 'stat -c %u:%g /data/lydiabox/home /data/workspace | sort -u')
	[ "$owner" = 1000:1000 ] || fail "/data/lydiabox/home and /data/workspace owner $owner, want 1000:1000"
	[ "$(in_ctr 'stat -c %a /data/lydiabox/home')" = 700 ] || fail "/data/lydiabox/home is not mode 0700"
	in_ctr 'test -f /data/lydiabox/home/.profile && test -e /data/lydiabox/home-seeded' || fail "the home seed was not copied"
	if [ "$current" != plain ]; then
		# shellcheck disable=SC2016 # expanded in the container
		sum='cd /data/home && { find . -path ./.cache -prune -o -printf "%y %m %U %p %l\n" | sort &&
			find . -path ./.cache -prune -o -type f -exec md5sum {} + | sort; }'
		if ! in_ctr "$sum | cmp -s - /var/lib/instabox-model/home.sum"; then
			in_ctr "$sum | diff /var/lib/instabox-model/home.sum - | head -20" >&2 || true
			fail "InstaBox's home /data/home changed"
		fi
		# Claude Code writes its own ~/.claude.json (check_harnesses runs
		# it); InstaBox's has its MCP servers.
		in_ctr '! test -e /data/lydiabox/home/AGENTS.md && ! test -e /data/lydiabox/home/.agents &&
			! grep -qs -e instabox -e open-computer-use /data/lydiabox/home/.claude.json' ||
			fail "InstaBox's files are in LydiaBox's home"
	fi
	if [ "$current" = migrate ]; then
		n=$(in_ctr 'wc -l </data/lydiabox/home-moved.log')
		[ "$n" -eq 1 ] || fail "the old layout moved $n times, want 1"
		n=$(docker logs "$name" 2>&1 | grep -c 'moved the agent home') || true
		[ "$n" -eq 1 ] || fail "$n log lines 'moved the agent home', want 1"
		in_ctr 'test -f /data/home/.claude/projects/-data-workspace/old-session.jsonl' || fail "an old LydiaBox file in /data/home was moved"
	elif in_ctr 'test -e /data/lydiabox/home-moved.log'; then
		fail "the start moved the home of a new disk"
	fi
}

# The X11 windows of type DIALOG on :0, one "ID WM_CLASS" line each.
# shellcheck disable=SC2016 # expanded in the container
dialog_windows='ids=$(xprop -root _NET_CLIENT_LIST | sed -n "s/.*# //p" | tr -d ,)
for w in $ids; do
	if xprop -id "$w" _NET_WM_WINDOW_TYPE | grep -q _NET_WM_WINDOW_TYPE_DIALOG; then
		echo "$w $(xprop -id "$w" WM_CLASS | cut -d= -f2)"
	fi
done'

# check_desktop: the panel (when the image has one) runs every plugin in
# its own process and names no missing plugin, and no dialog window is
# open (the defect: "Are you sure that you want to remove ...?" of a
# wrapper-2.0 plugin at each start).
check_desktop() {
	if in_ctr 'command -v xfce4-panel' >/dev/null 2>&1; then
		wait_for 'pgrep -u 1000 -x xfce4-panel' "xfce4-panel runs" 60
		sleep 10
		if in_ctr 'pgrep -af "xfce4/panel/wrapper-2[.]0"' >&2; then
			fail "a panel plugin runs in wrapper-2.0, outside the panel's process"
		fi
		if in_ctr "grep 'was not found' /var/log/supervisor/xfce.log" >&2; then
			fail "the panel settings name a plugin that is not installed"
		fi
	else
		sleep 5
	fi
	as_user=1000
	dialogs=$(in_ctr "DISPLAY=:0; export DISPLAY; $dialog_windows") || fail "cannot list the windows on :0"
	as_user=0
	[ -z "$dialogs" ] || fail "dialog windows on :0: $dialogs"
	echo "ok ($current): no dialog window on :0, no panel plugin outside the panel's process"
}

# check_harnesses: claude and codex on PATH are the image's, and with
# HOME=/home/agent (as in LydiaBox's PTYs) neither loads an InstaBox MCP
# server. The slim image has no harness CLIs.
check_harnesses() {
	if ! in_ctr 'command -v claude && command -v codex' >/dev/null 2>&1; then
		echo "SKIP ($current): the image has no harness CLIs (slim)"
		return
	fi
	for h in claude codex; do
		[ "$(in_ctr "readlink -f \"\$(command -v $h)\"")" = "$(in_ctr "readlink -f /usr/bin/$h")" ] ||
			fail "$h on PATH is not the image's /usr/bin/$h"
	done
	out=$(docker exec -u 1000 -e HOME=/home/agent -w /data/workspace "$name" \
		sh -c 'timeout 60 codex mcp list 2>&1; echo "--"; timeout 90 claude mcp list 2>&1' || true)
	if echo "$out" | grep -qiE 'instabox|open-computer-use'; then
		echo "$out" >&2
		fail "a harness loads an InstaBox MCP server"
	fi
	echo "ok ($current): codex and claude are the image's and load no InstaBox MCP server"
}

# check_session_bus: Xfce runs on LydiaBox's own session bus, and xfconfd
# (started by that bus) has HOME=/home/agent, so the Xfce settings are in
# LydiaBox's home. On the model, the image's settings replace the
# template's and the template's are kept once.
check_session_bus() {
	as_user=1000
	# shellcheck disable=SC2016 # expanded in the container
	wait_for 'pid=$(pgrep -u 1000 -x xfce4-session | head -n 1); tr "\0" "\n" </proc/$pid/environ | grep -qx DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/lydiabox-session-bus' "Xfce uses LydiaBox's session bus" 30
	# shellcheck disable=SC2016
	wait_for 'pid=$(pgrep -u 1000 -x xfconfd | head -n 1); tr "\0" "\n" </proc/$pid/environ | grep -qx HOME=/home/agent' "xfconfd has HOME=/home/agent" 30
	as_user=0
	in_ctr 'test -f /data/lydiabox/home/.config/xfce4/xfconf/xfce-perchannel-xml/xfce4-panel.xml' ||
		fail "the panel settings are not in LydiaBox's home"
	in_ctr '! grep -q mcp_servers /etc/codex/config.toml' || fail "/etc/codex/config.toml has MCP servers"
	if [ "$current" != plain ]; then
		in_ctr 'grep -q mcp_servers.instabox /etc/codex/config.toml.instabox-template' || fail "the template's Codex settings were not kept"
	fi
	echo "ok ($current): Xfce on LydiaBox's session bus, settings in LydiaBox's home, the image's Codex settings"
}
