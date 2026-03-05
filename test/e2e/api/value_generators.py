"""Dynamic value generators for RESTler custom payloads.

Generates a CSR from the enrollment key mounted at
/work/certs/client-enrollment.key.
"""

import base64
from pathlib import Path
import subprocess

KEY_PATH = Path("/work/certs/client-enrollment.key")
ENROLLMENT_IDENTITY = "fuzz-test-string"
OPENSSL_TIMEOUT_SECONDS = 15


def _run_openssl(command, *, input_bytes=None):
    try:
        return subprocess.run(
            command,
            input=input_bytes,
            capture_output=True,
            check=True,
            timeout=OPENSSL_TIMEOUT_SECONDS,
        )
    except FileNotFoundError as exc:
        raise RuntimeError("openssl is required for RESTler CSR generation") from exc
    except subprocess.TimeoutExpired as exc:
        raise RuntimeError(f"openssl command timed out: {' '.join(command)}") from exc
    except subprocess.CalledProcessError as exc:
        stderr = exc.stderr.decode("utf-8", errors="replace").strip()
        raise RuntimeError(f"openssl command failed: {' '.join(command)}: {stderr}") from exc


def _generate_csr_bytes():
    if not KEY_PATH.is_file():
        raise RuntimeError(f"Enrollment key file not found: {KEY_PATH}")

    create_result = _run_openssl(
        [
            "openssl",
            "req",
            "-new",
            "-batch",
            "-sha256",
            "-key",
            str(KEY_PATH),
            "-subj",
            f"/CN={ENROLLMENT_IDENTITY}",
        ]
    )
    csr_bytes = create_result.stdout
    if not csr_bytes:
        raise RuntimeError("openssl returned empty CSR output")

    _run_openssl(
        ["openssl", "req", "-verify", "-noout", "-in", "/dev/stdin"],
        input_bytes=csr_bytes,
    )
    return csr_bytes


CSR_BYTES = _generate_csr_bytes()


def gen_csr_pem(**kwargs):
    """Yield CSR as PEM with escaped newlines (for EnrollmentRequest.spec.csr)."""
    yield CSR_BYTES.decode("utf-8").replace("\n", "\\n").rstrip("\\n")


def gen_csr_base64(**kwargs):
    """Yield CSR as base64 (for CertificateSigningRequest.spec.request)."""
    yield base64.b64encode(CSR_BYTES).decode("ascii")


value_generators = {
    "restler_custom_payload": {
        "/spec/csr": gen_csr_pem,
        "/spec/request": gen_csr_base64,
    }
}
