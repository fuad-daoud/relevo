package spawn

// ReapFragment is the supervisor's scope reap: it TERMs, then KILLs, every
// other pid in the scope's cgroup, so no straggler keeps the scope alive. The
// file is read with the builtin read, never cat -- a cat subprocess would join
// the cgroup being reaped -- and every error is discarded, so an unemptiable
// scope never costs the exit trailer.
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

// SampleAggFragment aggregates one memsample file the way the trailer does:
// per-line anon+swap sums, sorted, give the median, the max, and five seconds
// per sample above half the max (the 5 is the sampler's sleep). A named
// function, not inline code, so a test runs the exact shell the trailer
// prints; an empty file still prints nothing and exits non-zero.
const SampleAggFragment = `relevo_sample_agg() {
  awk '{print $1+$2}' "$1" 2>/dev/null | sort -n 2>/dev/null | awk '{a[NR]=$1; if($1>m)m=$1} END{if(NR==0)exit 1; b=a[int((NR+1)/2)]; c=0; for(i=1;i<=NR;i++) if(a[i]>m/2)c++; printf "%d %d %d", b, m, c*5}' 2>/dev/null
}
`

// PressureTotalFragment prints one pressure file's total for the named class,
// some or full: the two lines carry independent counters, so the trailer asks
// for each by name. A named function, not inline code, so a test runs the
// exact shell against a synthetic file.
const PressureTotalFragment = `relevo_pressure_total() {
  awk -v want="$2" '$1==want{for(i=1;i<=NF;i++) if($i~/^total=/){sub(/^total=/,"",$i); print $i; exit}}' "$1" 2>/dev/null
}
`

// SupervisorScript runs the builder with stdin closed and appends the trailer
// whatever happens to it; plain sh, no bash-isms. Its first argument is the
// scope unit, or "" for a plain spawn. It raises oom_score_adj so the kernel
// prefers a builder over the daemon under memory pressure, and keeps the
// builder a child (no exec) so an OOM kill still leaves a trailer. Only when
// its own cgroup matches that unit does it print a rusage line and reap the
// scope: a plain spawn from inside a round's scope inherits that cgroup too.
// The reap's self pid comes from /proc/self/stat, never $$, which systemd-run's
// unit syntax rewrites and which would make the supervisor reap itself; a TERM
// exits 143 before the trailer.
const SupervisorScript = ReapFragment + SampleAggFragment + PressureTotalFragment + `want=$1
shift
trap 'kill "$sampjob" 2>/dev/null; [ -n "$samp" ] && rm -f "$samp" 2>/dev/null; exit 143' TERM
{ echo 500 >/proc/self/oom_score_adj; } 2>/dev/null || true
samp=""; sampjob=""
if [ -n "$want" ]; then
  prematch=$(cut -d: -f3 /proc/self/cgroup 2>/dev/null | head -1)
  case "$prematch" in
    */"$want")
      read -r sampself _ </proc/self/stat 2>/dev/null
      if [ -n "$sampself" ]; then
        samp="${TMPDIR:-/tmp}/relevo-memsamp-$sampself.log"
        ( while :; do
          a=$(awk '/^anon /{print $2; exit}' "/sys/fs/cgroup$prematch/memory.stat" 2>/dev/null)
          s=$(cat "/sys/fs/cgroup$prematch/memory.swap.current" 2>/dev/null)
          if [ -z "$a" ] || [ -z "$s" ]; then exit 0; fi
          printf '%s %s\n' "$a" "$s" >>"$samp" 2>/dev/null || exit 0
          sleep 5 2>/dev/null || exit 0
        done ) &
        sampjob=$!
      fi
      ;;
  esac
fi
"$@" </dev/null; rc=$?
if [ -n "$sampjob" ]; then kill "$sampjob" 2>/dev/null || true; wait "$sampjob" 2>/dev/null || true; fi
if [ -n "$want" ]; then
  cg=$(cut -d: -f3 /proc/self/cgroup 2>/dev/null | head -1)
  case "$cg" in */"$want")
    u=$(awk '/^usage_usec/{print $2}' "/sys/fs/cgroup$cg/cpu.stat" 2>/dev/null)
    m=$(cat "/sys/fs/cgroup$cg/memory.peak" 2>/dev/null)
    base=""; burst=""; bsecs=""
    if [ -f "$samp" ]; then
      agg=$(relevo_sample_agg "$samp" 2>/dev/null)
      if [ -n "$agg" ]; then
        base=${agg%% *}; tmp=${agg#* }; burst=${tmp%% *}; bsecs=${tmp#* }
      fi
    fi
    cs=$(relevo_pressure_total "/sys/fs/cgroup$cg/cpu.pressure" some 2>/dev/null)
    cf=$(relevo_pressure_total "/sys/fs/cgroup$cg/cpu.pressure" full 2>/dev/null)
    ms=$(relevo_pressure_total "/sys/fs/cgroup$cg/memory.pressure" some 2>/dev/null)
    mf=$(relevo_pressure_total "/sys/fs/cgroup$cg/memory.pressure" full 2>/dev/null)
    ios=$(relevo_pressure_total "/sys/fs/cgroup$cg/io.pressure" some 2>/dev/null)
    iof=$(relevo_pressure_total "/sys/fs/cgroup$cg/io.pressure" full 2>/dev/null)
    eh=$(awk '/^high /{print $2; exit}' "/sys/fs/cgroup$cg/memory.events" 2>/dev/null)
    em=$(awk '/^max /{print $2; exit}' "/sys/fs/cgroup$cg/memory.events" 2>/dev/null)
    ek=$(awk '/^oom_kill /{print $2; exit}' "/sys/fs/cgroup$cg/memory.events" 2>/dev/null)
    printf '\nrelevo-rusage:%s%s%s%s%s%s%s%s%s%s%s%s%s%s\n' "${u:+cpu_usec=$u}" "${m:+ mem_peak=$m}" "${base:+ base=$base}" "${burst:+ burst=$burst}" "${bsecs:+ burst_secs=$bsecs}" "${cs:+ cpu_some_usec=$cs}" "${cf:+ cpu_full_usec=$cf}" "${ms:+ mem_some_usec=$ms}" "${mf:+ mem_full_usec=$mf}" "${ios:+ io_some_usec=$ios}" "${iof:+ io_full_usec=$iof}" "${eh:+ ev_high=$eh}" "${em:+ ev_max=$em}" "${ek:+ ev_oom_kill=$ek}"
    read -r self _ </proc/self/stat
    [ -n "$self" ] && relevo_reap_scope "/sys/fs/cgroup$cg/cgroup.procs" "$self"
    ;;
  esac
fi
[ -n "$samp" ] && rm -f "$samp" 2>/dev/null || true
printf '\nrelevo-exit:%s\n' "$rc"`

// ContainerSupervisorScript is SupervisorScript with one added line: it exits
// with the builder's own code, so `podman run` returns it and the outer
// supervisor's trailer carries it rather than sh's zero.
const ContainerSupervisorScript = SupervisorScript + "\nexit \"$rc\""
