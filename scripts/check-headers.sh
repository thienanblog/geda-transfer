#!/usr/bin/env bash
# Copyright 2026 Geda
# SPDX-License-Identifier: Apache-2.0
#
# Every source file carries the Apache-2.0 header (AGENTS.md section 4).
# Fails with the list of files that do not.

set -euo pipefail
cd "$(dirname "$0")/.."

missing=()
while IFS= read -r file; do
    if ! head -20 "$file" | grep -qE 'Apache License, Version 2\.0|SPDX-License-Identifier: Apache-2\.0'; then
        missing+=("$file")
    fi
done < <(git ls-files '*.go' '*.sql' '*.sh' '*.ts' '*.tsx')

if [ ${#missing[@]} -gt 0 ]; then
    echo "Missing Apache-2.0 header:"
    printf '  %s\n' "${missing[@]}"
    exit 1
fi

echo "All source files carry the Apache-2.0 header."
