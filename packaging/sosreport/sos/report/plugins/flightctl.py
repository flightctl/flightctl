from sos.report.plugins import Plugin, RedHatPlugin

class Flightctl(Plugin, RedHatPlugin):
    plugin_name = "flightctl"
    short_desc = "Flight control"
    profiles = ('system', 'services')

    def setup(self):
        self.add_copy_spec([
            "/etc/flightctl",
            "/var/lib/flightctl"
        ])
        self.add_forbidden_path("/etc/flightctl/certs")
        self.add_forbidden_path("/var/lib/flightctl/certs")

    def postproc(self):
        regexp = r"((?:client-certificate-data|client-key-data|certificate-authority-data):\s*)\S+"
        self.do_file_sub(r"/etc/flightctl/config.yaml", regexp, r"\1********")
