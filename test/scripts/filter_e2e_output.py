#!/usr/bin/env python3

import xml.etree.ElementTree as ET
import argparse
import os

# Default file paths
report_file_path = 'junit_e2e_test.xml'
DEFAULT_INPUT = os.path.join('reports', report_file_path)
DEFAULT_OUTPUT = DEFAULT_INPUT

# Substrings to filter from <testcase name>
FILTER_NAME_SUBSTRINGS = [
    "[BeforeSuite]",
    "[AfterSuite]",
    "[DeferCleanup (Suite)]",
    "[DeferCleanup (Container)]",
    "Extension",
    "Label Selectors",
    "Basic Operations",
]

def filter_junit_xml(input_path, output_path, name_substrings):
    try:
        tree = ET.parse(input_path)
        root = tree.getroot()

        filtered_testcases_count = 0
        removed_testcase_names = []

        for testsuite in root.findall('testsuite'):
            testcases_to_remove = []
            for testcase in testsuite.findall('testcase'):
                name = testcase.get('name', '')
                if any(keyword in name for keyword in name_substrings):
                    testcases_to_remove.append(testcase)
                    removed_testcase_names.append(name)

            for testcase in testcases_to_remove:
                testsuite.remove(testcase)
                filtered_testcases_count += 1

            # Update the count of tests in the testsuite
            current_tests = int(testsuite.get('tests', '0'))
            testsuite.set('tests', str(current_tests - len(testcases_to_remove)))

        # Write the filtered tree to output
        tree.write(output_path, encoding='utf-8', xml_declaration=True)

        print(f"✅ Removed {filtered_testcases_count} test case(s) by name match.")
        if removed_testcase_names:
            print("🗑️ Removed test cases:")
            for name in removed_testcase_names:
                print(f"  - {name}")
        print(f"📄 Output saved to: {output_path}")

    except FileNotFoundError:
        print(f"❌ Error: File not found at {input_path}")
    except ET.ParseError as e:
        print(f"❌ XML Parse Error: {e}")
    except Exception as e:
        print(f"❌ Unexpected error: {e}")

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Remove test cases from JUnit XML by partial name match.")
    parser.add_argument(
        "--input-file",
        default=DEFAULT_INPUT,
        help=f"Path to input JUnit XML file (default: {DEFAULT_INPUT})"
    )
    parser.add_argument(
        "--output-file",
        default=DEFAULT_OUTPUT,
        help=f"Path to save filtered JUnit XML file (default: {DEFAULT_OUTPUT})"
    )
    args = parser.parse_args()

    filter_junit_xml(args.input_file, args.output_file, FILTER_NAME_SUBSTRINGS)
