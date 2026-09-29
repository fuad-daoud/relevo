#!/usr/bin/env bash
# tui.sh: run a terminal UI in a detached tmux session, drive it with keys,
# and capture what it draws as text, ANSI or a PNG.
#   tui.sh start NAME WxH CMD [ARGS...]   start CMD in a WxH pane (truecolor)
#   tui.sh keys  NAME KEY...              tmux key names (Enter, Escape, Down, C-c)
#                                         or text:LITERAL to type a string
#   tui.sh wait  NAME REGEX [SECS]        block until REGEX is on screen (default 10s)
#   tui.sh gone  NAME REGEX [SECS]        block until REGEX is off screen
#   tui.sh text  NAME                     print the screen as plain text
#   tui.sh ansi  NAME                     print the screen with colour escapes
#   tui.sh png   NAME OUT.png             render the screen, colours included, to a PNG
#   tui.sh stop  NAME                     kill the session
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
sub=${1:?usage: tui.sh start|keys|wait|gone|text|ansi|png|stop NAME ...}; shift
name=${1:?NAME required}; shift

screen() { tmux capture-pane -t "$name" -p "$@"; }

until_screen() { # want(0=present,1=absent) regex secs
	local want=$1 re=$2 secs=${3:-10} end=$((SECONDS + ${3:-10})) hit
	while :; do
		if screen | grep -qE -- "$re"; then hit=0; else hit=1; fi
		[ "$hit" -eq "$want" ] && return 0
		if [ "$SECONDS" -ge "$end" ]; then
			echo "tui.sh: /$re/ still $([ "$want" -eq 0 ] && echo absent || echo present) after ${secs}s; screen:" >&2
			screen >&2
			return 1
		fi
		sleep 0.2
	done
}

case "$sub" in
start)
	size=${1:?WxH required}; shift
	tmux kill-session -t "$name" 2>/dev/null || true
	tmux new-session -d -s "$name" -x "${size%x*}" -y "${size#*x}" \
		-e COLORTERM=truecolor -e TERM=xterm-256color "$@"
	;;
keys)
	for k in "$@"; do
		case "$k" in
		text:*) tmux send-keys -t "$name" -l "${k#text:}" ;;
		*) tmux send-keys -t "$name" "$k" ;;
		esac
		sleep 0.15
	done
	;;
wait) until_screen 0 "${1:?REGEX required}" "${2:-10}" ;;
gone) until_screen 1 "${1:?REGEX required}" "${2:-10}" ;;
text) screen ;;
ansi) screen -e ;;
png)
	out=$(realpath -m "${1:?OUT.png required}")
	read -r cols rows < <(tmux display -p -t "$name" '#{pane_width} #{pane_height}')
	screen -e | python3 "$here/ansi2html.py" > "${out%.png}.html"
	browser=$(command -v chromium || command -v google-chrome || command -v chrome) ||
		{ echo "tui.sh: no chromium/chrome for png; use text or ansi" >&2; exit 1; }
	"$browser" --headless --disable-gpu --hide-scrollbars --force-device-scale-factor=1 \
		--window-size=$((cols * 9 + 32)),$((rows * 19 + 32)) \
		--screenshot="$out" "file://${out%.png}.html" >/dev/null 2>&1
	echo "$out"
	;;
stop) tmux kill-session -t "$name" ;;
*) echo "tui.sh: unknown subcommand $sub" >&2; exit 2 ;;
esac
