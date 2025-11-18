from sos.report.plugins import Plugin, RedHatPlugin, PluginOpt
import shlex

class Flightctl(Plugin, RedHatPlugin):
    plugin_name = "flightctl"
    short_desc = "Flight control"
    profiles = ('system', 'services')

    # Plugin option for journal time window
    option_list = [
        PluginOpt(
            "journal_since",
            default="-24h",
            desc="Time range for journalctl --since (e.g. '2 hours ago')"
        )
    ]

    def setup(self):
        self.add_copy_spec([
            "/etc/flightctl",
            "/var/lib/flightctl",
            "/var/log/flightctl"
        ])
        self.add_forbidden_path("/etc/flightctl/certs")
        self.add_forbidden_path("/var/lib/flightctl/certs")

        # Prometheus metrics
        self.add_cmd_output(
            "curl -fsS --max-time 10 -H 'Accept: text/plain' 'http://127.0.0.1:15690/metrics'",
            suggest_filename="flightctl-metrics.txt",
        )

        # Goroutines
        self.add_cmd_output(
            "curl -fsS --max-time 10 'http://127.0.0.1:15689/debug/pprof/goroutine?debug=2'",
            suggest_filename="flightctl-goroutines.txt",
        )

        # Heap (binary)
        self.add_cmd_output(
            "curl -fsS --max-time 10 'http://127.0.0.1:15689/debug/pprof/heap'",
            suggest_filename="flightctl-pprof-heap.pprof", binary=True, to_file=True
        )

        # CPU profile 5s (binary)
        self.add_cmd_output(
            "curl -fsS --max-time 10 'http://127.0.0.1:15689/debug/pprof/profile?seconds=5'",
            suggest_filename="flightctl-pprof-cpu.pprof", binary=True, to_file=True
        )

        # Journal logs (configurable --since, defaults to 24 hours)
        since = self.get_option("journal_since")
        since_q = shlex.quote(since)

        self.add_cmd_output(
            f"journalctl -u flightctl-agent.service --since {since_q} --no-pager",
            suggest_filename="flightctl-agent-journal.txt"
        )

    def postproc(self):
        regexp = r"((?:client-certificate-data|client-key-data|certificate-authority-data):\s*)\S+"
        self.do_file_sub(r"/etc/flightctl/config.yaml", regexp, r"\1********")
