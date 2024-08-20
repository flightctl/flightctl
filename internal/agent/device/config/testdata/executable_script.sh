#!/usr/bin/env bash

if [ "$#" -ne 2 ]; then
    echo "Usage: $0 <file_path> <file_content>"
    exit 1
fi

file_path=$1
file_content=$2

echo "Writing content to file ${file_path}"

# Write the content to the file
echo "${file_content}" > "${file_path}"

# Check if the file was written successfully
if [ $? -eq 0 ]; then
    echo "File written successfully to ${file_path}"
else
    echo "Failed to write file to ${file_path}"
    exit 1
fi