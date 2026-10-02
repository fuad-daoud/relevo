package spawn

// ReapFragment is the supervisor's scope reap: a POSIX sh function
// relevo_reap_scope <procs_file> <self_pid> that TERMs, then KILLs, every other
// pid in the scope's cgroup, so a straggler the harness abandoned cannot keep
// the scope alive. The file is read with the builtin read, never cat -- a cat
// subprocess would join the cgroup being reaped -- and every error is
// discarded, so an unemptiable scope never costs the builder its exit trailer.
const ReapFragment = `relevo_reap_scope() {
  procs=$1
  self=$2
  while read -r pid; do
    [ "$pid" = "$self" ] || kill -TERM "$pid" 2>/dev/null
  done 2>/dev/null <"$procs"
  i=0
  while [ "$i" -lt 20 ]; do
    left=0
    while read -r pid; do
      [ "$pid" = "$self" ] || left=1
    done 2>/dev/null <"$procs"
    [ "$left" = 0 ] && break
    sleep 0.1
    i=$((i + 1))
  done
  while read -r pid; do
    [ "$pid" = "$self" ] || kill -KILL "$pid" 2>/dev/null
  done 2>/dev/null <"$procs"
  return 0
}
`

// SupervisorScript runs the builder with stdin closed and appends the trailer
// whatever happens to it; plain sh, no bash-isms. Its first argument is the
// scope unit Start expects this process to run in, or "" for a plain spawn.
// It raises oom_score_adj so the kernel prefers a builder over the daemon under
// memory pressure, and keeps the builder a child (no exec) so an OOM kill still
// leaves a trailer. Only when its own cgroup matches that unit does it print a
// rusage line and reap the scope: a plain spawn from inside a round's scope
// inherits that cgroup too. The reap's self pid comes from /proc/self/stat,
// never $$, which systemd-run's unit syntax rewrites to a single $ and which
// would make the supervisor reap itself; a TERM exits 143 before the trailer.
const SupervisorScript = ReapFragment + `want=$1
shift
trap 'exit 143' TERM
{ echo 500 >/proc/self/oom_score_adj; } 2>/dev/null || true
"$@" </dev/null; rc=$?
if [ -n "$want" ]; then
  cg=$(cut -d: -f3 /proc/self/cgroup 2>/dev/null | head -1)
  case "$cg" in */"$want")
    u=$(awk '/^usage_usec/{print $2}' "/sys/fs/cgroup$cg/cpu.stat" 2>/dev/null)
    m=$(cat "/sys/fs/cgroup$cg/memory.peak" 2>/dev/null)
    printf '\nrelevo-rusage:%s%s\n' "${u:+cpu_usec=$u}" "${m:+ mem_peak=$m}"
    read -r self _ </proc/self/stat
    [ -n "$self" ] && relevo_reap_scope "/sys/fs/cgroup$cg/cgroup.procs" "$self"
    ;;
  esac
fi
printf '\nrelevo-exit:%s\n' "$rc"`

// ContainerSupervisorScript is SupervisorScript with one added line: it exits
// with the builder's own code. A container round runs this script as the
// container command, so `podman run` returns that code to the outer supervisor,
// whose last-line trailer is then the builder's code rather than sh's zero.
const ContainerSupervisorScript = SupervisorScript + "\nexit \"$rc\""
