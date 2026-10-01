# relevo 0.4.2: internal in send

created: 2026-09-30T00:43:12Z
relevo: 0.4.2
privacy: redacted: home paths to ~, the user name to <user>, the host name to <host>, remotes and secret-shaped strings to placeholders

## Environment

| fact | value |
| --- | --- |
| relevo version | 0.4.2 |
| relevo distribution | release |
| go version | go1.27.0 |
| goos | linux |
| goarch | amd64 |
| state root | ~/.local/state/relevo |

## Last error

| fact | value |
| --- | --- |
| time | 2026-09-30T00:41:42Z |
| verb | send |
| argv | relevo send --name alpha --round 3 |
| code | internal |
| message | write ~/.local/state/relevo/alpha/003-runner.jsonl: boom on <host> |
| next | relevo bugreport |

## Doctor

usable builder: true
no candidates: false
builder refusal: 
failures: 1
warnings: 1

| group | name | severity | detail | fix | probe_failed |
| --- | --- | --- | --- | --- | --- |
| claude | binary | warn | not on PATH | install claude | false |
|  | daemon | fail | not running | relevo daemon | false |
|  | release | ok | 0.4.2 is current |  | false |

## Status

| binding | actor | candidate | state | round | rounds | shape | where | pending |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| alpha | builder | claude:sonnet | running | 3 | 3 |  | local | report |
| beta | builder | claude:haiku | done | 1 | 1 | reader | remote |  |

## Rounds

| binding | seq | ts | round | direction | kind | route | confirmed | late | tier | outcome | halted_at | note | tokens_in | tokens_cache_read | tokens_cache_write | tokens_out |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| alpha | 1 | 2026-09-30T00:13:12Z | 3 | to_runner | prompt | deliverer | true | false |  |  |  | round 3 sent with <redacted> | 0 | 0 | 0 | 0 |
| alpha | 2 | 2026-09-30T00:33:12Z | 3 | to_planner | report |  | true | true | plan | done |  |  | 1200 | 100 | 0 | 30 |
| alpha | 3 | 2026-09-30T00:34:12Z | 3 | to_planner | diff | pull | false | false |  |  |  | diff recorded for round 3 | 0 | 0 | 0 | 0 |
| beta | 1 | 2026-09-28T00:43:12Z | 1 | to_runner | prompt | channel | true | false |  |  |  |  | 0 | 0 | 0 | 0 |

## Hooks

| at | event | hook | exit_code | error |
| --- | --- | --- | --- | --- |
| 2026-09-30T00:38:12Z | state_changed | notify | 0 |  |
| 2026-09-30T00:39:12Z | round_started | hook | 1 | boom: <redacted> and <redacted> |

## Gates

| kind | subject | at | until | source | binding | note |
| --- | --- | --- | --- | --- | --- | --- |
| rate_limited | claude:sonnet | 2026-09-30T00:23:12Z | 2026-09-30T01:23:12Z | relevo | alpha | usage limit: <redacted> |
| spawn_failed | claude:haiku | 2026-09-29T22:43:12Z |  | relevo | beta | <redacted> and <redacted> and <redacted> |

## Daemon

| fact | value |
| --- | --- |
| running | true |
| version | 0.4.2 |
| pid | 4242 |
| started_at | 2026-09-29T21:43:12Z |
| exe | ~/.local/bin/relevo |
