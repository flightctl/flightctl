#!/usr/bin/env bash

URL=$1

mkdir tmp_git_dir
cd tmp_git_dir

git init -b main --quiet
git remote add origin $URL
git pull origin main --quiet
git fetch --tags --quiet
git describe --tags --exclude latest > ../source_git_tag.txt
git rev-parse --short "HEAD^{commit}" 2>/dev/null > ../source_git_commit.txt

cd ..
rm -rf tmp_git_dir
