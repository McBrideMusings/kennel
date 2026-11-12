---
name: project-perf
description: "kennel: the cost scenario (kennel env latency on every cd) a diff must run after project-verify, and the limit that fails it."
---

# kennel perf

The zsh hook runs `kennel env` on every `cd` in every shell, so its time per call is a delay
the user feels on each directory change. Nothing else kennel does runs often enough to matter.

| Scenario | Command | Run for |
| --- | --- | --- |
| env latency | `bash .claude/skills/project-perf/tools/env-latency.sh [limit-ms]` | any diff to `main.go` `cmdEnv`, `routes.go`, `config.go` |

## env latency

Fixture: a throwaway `HOME`, a two-tool config, 2001 routes, and a git repo whose
subdirectory the last route matches. The script times 100 calls of `kennel env` there.

| Measure | Limit | Today |
| --- | --- | --- |
| ms per `kennel env` call, 2000 routes | none set | 40.5–157 |
| same, 1 route | — | 40–42 |
| bare `git rev-parse --git-common-dir`, same moment | — | 30–32 |

Today's readings come from a machine at load average 290–358 on 14 cores, where a bare
`/usr/bin/true` took 5–6 ms. A limit gets set from a run at load under the core count, with a
known-broken build (`KENNEL_BIN=<path>`) timed beside it; the limit sits between the two.

A run on a machine whose load average is over its core count (`sysctl -n vm.loadavg hw.ncpu`)
is rerun once before a failure counts.
