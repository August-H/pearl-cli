#!/bin/sh

set -eu

usage() {
	cat <<'EOF'
Usage: ./testenv/monitor-daemon.sh [options]

Build and run a Pearl daemon with isolated state, then record its CPU and RAM use.

Options:
  --duration SECONDS  Monitoring time. Use 0 to run until Ctrl-C. Default: 60
  --interval SECONDS  Delay between samples. Decimal values work. Default: 1
  --output DIRECTORY  Result directory. Default: test-results/daemon-monitor-<time>-<pid>
  -h, --help          Show this help
EOF
}

fail() {
	printf 'daemon monitor: %s\n' "$*" >&2
	exit 2
}

is_interval() {
	awk -v value="$1" 'BEGIN {
		exit !(value ~ /^[0-9]+([.][0-9]+)?$/ && value > 0)
	}'
}

script_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(CDPATH= cd -- "$script_directory/.." && pwd)
duration=60
interval=1
output_directory=

while [ "$#" -gt 0 ]; do
	case "$1" in
	--duration)
		[ "$#" -ge 2 ] || fail "--duration needs a value"
		duration=$2
		shift 2
		;;
	--interval)
		[ "$#" -ge 2 ] || fail "--interval needs a value"
		interval=$2
		shift 2
		;;
	--output)
		[ "$#" -ge 2 ] || fail "--output needs a value"
		output_directory=$2
		shift 2
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		fail "unknown option: $1"
		;;
	esac
done

case "$duration" in
'' | *[!0-9]*) fail "--duration must be a whole number of seconds" ;;
esac
is_interval "$interval" || fail "--interval must be greater than zero"

LC_ALL=C
export LC_ALL

run_identifier=$(date -u '+%Y%m%dT%H%M%SZ')-$$
if [ -z "$output_directory" ]; then
	output_directory="$repository_root/test-results/daemon-monitor-$run_identifier"
else
	case "$output_directory" in
	/*) ;;
	*) output_directory="$PWD/$output_directory" ;;
	esac
fi

[ ! -e "$output_directory" ] || fail "output directory already exists: $output_directory"
mkdir -p -- "$output_directory"

runtime_directory=$(mktemp -d "${TMPDIR:-/tmp}/pearl-daemon-monitor.XXXXXX")
config_directory="$runtime_directory/config"
binary_path="$runtime_directory/pearl"
socket_path="$config_directory/pearl.sock"
daemon_log="$output_directory/daemon.log"
samples_file="$output_directory/samples.csv"
summary_file="$output_directory/summary.txt"
daemon_pid=

write_summary() {
	if [ ! -s "$samples_file" ]; then
		return 0
	fi
	awk -F, '
		NR == 1 { next }
		{
			count++
			last_elapsed = $2
			cpu_sum += $3
			rss_sum += $5
			if (count == 1 || $3 > cpu_peak) cpu_peak = $3
			if (count == 1 || $5 > rss_peak) rss_peak = $5
		}
		END {
			if (count == 0) exit
			printf "samples: %d\n", count
			printf "elapsed_seconds: %d\n", last_elapsed
			printf "average_cpu_percent: %.2f\n", cpu_sum / count
			printf "peak_cpu_percent: %.2f\n", cpu_peak
			printf "average_rss_mib: %.2f\n", rss_sum / count
			printf "peak_rss_mib: %.2f\n", rss_peak
		}
	' "$samples_file" >"$summary_file"
}

stop_daemon() {
	if [ -z "$daemon_pid" ]; then
		return 0
	fi
	if kill -0 "$daemon_pid" 2>/dev/null; then
		kill -TERM "$daemon_pid" 2>/dev/null || true
		attempt=0
		while kill -0 "$daemon_pid" 2>/dev/null && [ "$attempt" -lt 100 ]; do
			process_state=$(ps -p "$daemon_pid" -o stat= 2>/dev/null | awk 'NF { print $1; exit }')
			case "$process_state" in
			'' | *Z*) break ;;
			esac
			sleep 0.1
			attempt=$((attempt + 1))
		done
		process_state=$(ps -p "$daemon_pid" -o stat= 2>/dev/null | awk 'NF { print $1; exit }')
		case "$process_state" in
		'' | *Z*) ;;
		*)
			kill -KILL "$daemon_pid" 2>/dev/null || true
			;;
		esac
	fi
	wait "$daemon_pid" 2>/dev/null || true
	daemon_pid=
}

clean_up() {
	status=$?
	trap - EXIT
	stop_daemon
	write_summary
	rm -rf -- "$runtime_directory"
	if [ -f "$summary_file" ]; then
		printf '\nResults: %s\n' "$output_directory"
		cat "$summary_file"
	else
		rmdir "$output_directory" 2>/dev/null || true
	fi
	exit "$status"
}

trap clean_up EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

mkdir -m 700 -- "$config_directory"
cp "$script_directory/settings.json" "$config_directory/settings.json"
chmod 600 "$config_directory/settings.json"
printf '%s\n' 'OPENROUTER_API_KEY=disabled-in-daemon-monitor' >"$config_directory/.env"
chmod 600 "$config_directory/.env"

printf 'Building the test daemon...\n'
(
	cd "$repository_root"
	go build -o "$binary_path" ./cmd/pearl
)

export PEARL_CONFIG_DIR="$config_directory"
export PEARL_SOCKET="$socket_path"

"$binary_path" daemon run >"$daemon_log" 2>&1 &
daemon_pid=$!

ready=false
attempt=0
while [ "$attempt" -lt 100 ]; do
	if "$binary_path" daemon status >/dev/null 2>&1; then
		ready=true
		break
	fi
	if ! kill -0 "$daemon_pid" 2>/dev/null; then
		break
	fi
	sleep 0.1
	attempt=$((attempt + 1))
done

if [ "$ready" != true ]; then
	printf 'The test daemon did not become ready. Log: %s\n' "$daemon_log" >&2
	exit 1
fi

printf 'timestamp_utc,elapsed_seconds,cpu_percent,rss_kib,rss_mib\n' >"$samples_file"
printf 'Monitoring daemon PID %s every %s second(s).\n' "$daemon_pid" "$interval"
if [ "$duration" -eq 0 ]; then
	printf 'Duration is unlimited. Press Ctrl-C to stop.\n'
else
	printf 'Duration: %s seconds.\n' "$duration"
fi
printf 'Samples: %s\n' "$samples_file"
printf 'Daemon log: %s\n' "$daemon_log"

start_epoch=$(date '+%s')
sample_count=0
while :; do
	now_epoch=$(date '+%s')
	elapsed=$((now_epoch - start_epoch))
	if [ "$duration" -ne 0 ] && [ "$elapsed" -ge "$duration" ] && [ "$sample_count" -gt 0 ]; then
		break
	fi

	if ! kill -0 "$daemon_pid" 2>/dev/null; then
		printf 'The test daemon exited during monitoring. Log: %s\n' "$daemon_log" >&2
		daemon_pid=
		exit 1
	fi

	metrics=$(ps -p "$daemon_pid" -o %cpu= -o rss= 2>/dev/null | awk 'NF >= 2 { print $1 "," $2; exit }')
	if [ -z "$metrics" ]; then
		printf 'Could not read metrics for daemon PID %s.\n' "$daemon_pid" >&2
		exit 1
	fi

	cpu_percent=${metrics%%,*}
	rss_kib=${metrics#*,}
	rss_mib=$(awk -v rss="$rss_kib" 'BEGIN { printf "%.2f", rss / 1024 }')
	timestamp=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
	printf '%s,%s,%s,%s,%s\n' \
		"$timestamp" "$elapsed" "$cpu_percent" "$rss_kib" "$rss_mib" >>"$samples_file"
	sample_count=$((sample_count + 1))
	sleep "$interval"
done
