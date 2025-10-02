#!/usr/bin/env python3

import sys
import yaml
import json

try:
    for d in yaml.safe_load_all(sys.stdin):
        print(json.dumps(d))
except yaml.YAMLError as e:
    print(f"yaml-to-json: YAML error: {e}", file=sys.stderr)
except (TypeError, ValueError) as e:
    print(f"yaml-to-json: JSON conversion error: {e}", file=sys.stderr)
except Exception as e:
    print(f"yaml-to-json: Unexpected error: {e}", file=sys.stderr)
