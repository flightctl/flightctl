#!/usr/bin/env python3

import xml.etree.ElementTree as ET
import argparse
import os

# Default file paths
report_file_path = 'junit_e2e_test.xml'
DEFAULT_INPUT = os.path.join('reports', report_file_path)
DEFAULT_OUTPUT = DEFAULT_INPUT

# Substrings to identify and filter out in testcase names
FILTER_SUBSTRINGS = [
    "[BeforeSuite]",
    "[AfterSuite]",
    "[DeferCleanup (Suite)]",
    "[DeferCleanup (Container)]",
]

def filter_junit_xml(input_path, output_path, filter_strings):
    try:
        tree = ET.parse(input_path)
        root = tree.getroot()

        filtered_testcases_count = 0

        # Iterate through all <testsuite> elements
        for testsuite in root.findall('.//testsuite'):
            testcases_to_remove = []
            for testcase in testsuite.findall('testcase'):
                testcase_name = testcase.get('name', '')
                if any(f_str in testcase_name for f_str in filter_strings):
                    testcases_to_remove.append(testcase)
                    filtered_testcases_count += 1

            # Remove identified test cases
            for tc in testcases_to_remove:
                testsuite.remove(tc)

            # Update tests count in the <testsuite> tag
            current_tests = int(testsuite.get('tests', '0'))
            testsuite.set('tests', str(current_tests - len(testcases_to_remove)))

        # Write to output file
        tree.write(output_path, encoding='utf-8', xml_declaration=True)
        print(f"✅ Filtered {filtered_testcases_count} test case(s).")
        print(f"📄 Filtered JUnit XML saved to: {output_path}")

    except FileNotFoundError:
        print(f"❌ Error: Input file not found at {input_path}")
    except ET.ParseError as e:
        print(f"❌ Error parsing XML: {e}")
    except Exception as e:
        print(f"❌ Unexpected error: {e}")

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Filter unwanted test cases from a JUnit XML file.")
    parser.add_argument(
        "--input-file",
        default=DEFAULT_INPUT,
        help=f"Path to the input JUnit XML file (default: {DEFAULT_INPUT})"
    )
    parser.add_argument(
        "--output-file",
        default=DEFAULT_OUTPUT,
        help=f"Path to save the filtered output XML (default: {DEFAULT_OUTPUT})"
    )
    args = parser.parse_args()

    filter_junit_xml(args.input_file, args.output_file, FILTER_SUBSTRINGS)
