# CLI startup performance

Cold dashboard startup fell from 131.7 ms to 66.4 ms, a 49.6% reduction in
median time on this machine. Starting a daemon with a fresh database fell from
131.2 ms to 63.5 ms, a 51.6% reduction. Warm dashboard startup, help, and version
showed little change.

The comparison uses the original executable from commit
`c8a411cf5375d1496a67111c1313ba62d2a9cfb3` and an executable built with these
changes. Existing local Makefile changes and functional tests were preserved.

The implementation changes are:

- In `cli/daemon.go`, replace the fixed 100 ms pause between readiness requests
  with delays starting at 5 ms and doubling up to 100 ms. Fast starts are detected
  sooner, while slow starts still back off. The overall 20 second deadline now
  also bounds individual requests and waits. Readiness still requires a
  successful daemon status response.
- In `internal/store/sqlite.go`, create and upgrade the schema in one SQLite
  transaction. This reduces separate commits and rolls back partial schema
  changes on failure. Existing job-data migrations still run afterward.
- Read each table's columns once per startup, reducing `PRAGMA table_info`
  queries from 24 to 2. Required columns and legacy-data repairs still run on
  every open, retaining compatibility with existing databases.

End-to-end timings are in milliseconds. Each entry uses 30 measured samples per
binary, after one complete warm-up sample. Lower is better.

| Scenario | Before median | After median | Reduction | Before p95 | After p95 |
| --- | ---: | ---: | ---: | ---: | ---: |
| Dashboard, daemon stopped | 131.748 | 66.394 | 49.6% | 136.100 | 75.631 |
| Daemon start, fresh database | 131.195 | 63.480 | 51.6% | 136.759 | 72.779 |
| Daemon start, existing database | 130.845 | 65.407 | 50.0% | 134.583 | 75.975 |
| Dashboard, daemon already running | 24.909 | 23.894 | 4.1% | 30.992 | 31.196 |
| Help | 24.644 | 23.098 | 6.3% | 29.179 | 29.545 |
| Version | 23.144 | 23.054 | 0.4% | 29.541 | 28.476 |

The small differences in the last three rows are within observed process-launch
variation. The changes target daemon startup; these results do not establish an
improvement for commands that already have a running daemon or need none.

The database benchmark measures `store.Open` and `Close`, including migrations
and file-permission checks. These are medians across five alternating benchmark
runs per revision, each with a one second measurement period per case. Fresh
database deletion happens outside the timer.

| Database case | Before | After | Time reduction | Allocations before | Allocations after |
| --- | ---: | ---: | ---: | ---: | ---: |
| Fresh | 14.566 ms | 11.808 ms | 18.9% | 4,864 | 780 |
| Existing | 4.332 ms | 2.621 ms | 39.5% | 6,211 | 782 |

Existing-database allocation count fell 87.4%, and allocated bytes fell from
140,942 to 61,402 per open, a 56.4% reduction. These isolated database timings
explain part of the end-to-end improvement; the readiness interval explains the
larger share.

Verification completed successfully:

- `go vet ./...`
- `go test ./... -count=1 -timeout 5m`
- `go test -race ./... -count=1 -timeout 5m`
- `bash testenv/smoke.sh /tmp/pearl-startup-bench/pearl-after`

Both full test runs include the existing functional suite, which builds and
executes the real CLI and exercises background daemon startup, restart, durable
history, command behavior, and recovery. New regression tests cover readiness
retries, cancellation of an in-flight readiness request, expired deadlines, and
schema rollback after a failed migration. Existing legacy-schema and job-data
migration tests also pass.

Measurements ran on an Apple M4, macOS 15.8.2, arm64, with Go 1.26.4 and cgo.
Both CLI binaries used `go build -trimpath -ldflags '-s -w'`. Timing runs followed
test completion and alternated binary order to reduce ordering bias. Each
sample used a disposable home, config directory, workspace, database, and Unix
socket. Model catalogs were cached and no accounts were connected. Dashboard
timing ends when its command prompt appears in a 120 by 40 pseudo-terminal;
daemon timing ends when `daemon start` exits successfully. These are fresh
process measurements with a warm OS cache, not disk-cache-cold boot timings.
Large databases, uncached model catalogs, connected subscriptions, and Linux or
Windows performance were not measured here.

The reusable measurement script is `testenv/startup_benchmark.py`. After building
each revision to a separate executable, run:

```sh
python3 testenv/startup_benchmark.py \
  /tmp/pearl-startup-bench/pearl-before \
  /tmp/pearl-startup-bench/pearl-after \
  --samples 30 --output test-results/startup/startup-comparison.json

go test ./internal/store -run '^$' -bench '^BenchmarkOpen$' -benchmem -count=5
```

Local measurement artifacts are in the ignored `test-results/startup` directory:

- [Raw CLI samples and summaries](test-results/startup/startup-comparison.json)
- [Before database benchmark runs](test-results/startup/store-before-alternating.txt)
- [After database benchmark runs](test-results/startup/store-after-alternating.txt)
- [Race-test output](test-results/startup/race-tests.txt)
- [Startup comparison chart](test-results/startup/startup-comparison.html)
