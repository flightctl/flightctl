import subprocess
import time

subprocess.run(["podman", "run", "-d", "--name", "test_sleep", "alpine", "sleep", "100"])
time.sleep(1)
subprocess.run(["podman", "stop", "test_sleep"])
