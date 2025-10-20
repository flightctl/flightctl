#!/usr/bin/env python3

import sys
import yaml
import argparse

def extract_value_as_yaml(path, data):
    """
    Extract a value from nested data using a dot-separated path.
    If it's a simple value, return it as a string.
    If it's a complex structure, return it as YAML.
    Returns None if the path is not found.
    """
    path_parts = path.split('.')
    current = data
    
    for part in path_parts:
        if part == '':
            continue
            
        if isinstance(current, dict):
            if part in current:
                current = current[part]
            else:
                # Key not found
                return None
        else:
            return None

    if current is None:
        return None

    # If it's a complex structure, return as YAML
    if isinstance(current, (dict, list)):
        return yaml.dump(current, default_flow_style=False, allow_unicode=True, 
                        default_style=None, explicit_start=False, explicit_end=False).rstrip('\n')

    # Otherwise, it's a simple scalar value
    return str(current)

def _handle_error(error_msg, default_value):
    """Handle errors with optional default fallback."""
    if default_value is not None:
        print(default_value)
        sys.exit(0)
    else:
        print(f"yaml_helpers: {error_msg}", file=sys.stderr)
        sys.exit(1)

def extract_command(args):
    """Handle the extract subcommand."""
    path = args.key_path
    file_path = args.yaml_file
    default_value = args.default_value

    try:
        with open(file_path, 'r') as f:
            data = yaml.safe_load(f)

        if data is None:
            if default_value is not None:
                print(default_value)
            sys.exit(0)

        result = extract_value_as_yaml(path, data)
        
        if result is None:
            if default_value is not None:
                print(default_value)
            else:
                sys.exit(0)
        else:
            print(result)

    except FileNotFoundError:
        _handle_error(f"File not found: {file_path}", default_value)
    except yaml.YAMLError as e:
        _handle_error(f"YAML error: {e}", default_value)
    except Exception as e:
        _handle_error(f"Unexpected error: {e}", default_value)

def main():
    parser = argparse.ArgumentParser(
        prog='yaml_helpers',
        description='YAML processing utilities'
    )
    
    subparsers = parser.add_subparsers(
        dest='command',
        help='Available commands',
        required=True
    )
    
    # Extract subcommand
    extract_parser = subparsers.add_parser(
        'extract',
        help='Extract a value from a YAML file using a dot-separated path'
    )
    extract_parser.add_argument(
        'key_path',
        help='Dot-separated path to the value (e.g., .global.auth.type)'
    )
    extract_parser.add_argument(
        'yaml_file',
        help='Path to the YAML file to read'
    )
    extract_parser.add_argument(
        '--default',
        dest='default_value',
        help='Default value to return if the path is not found or on error'
    )
    extract_parser.set_defaults(func=extract_command)
    
    args = parser.parse_args()
    args.func(args)

if __name__ == "__main__":
    main()
