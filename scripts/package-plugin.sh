#!/usr/bin/env bash
set -euo pipefail

lib_dir="${1:?library directory is required}"
lib_name="${2:?library name is required}"
archive_name="${3:?archive name is required}"
repo_root="${GITHUB_WORKSPACE:-$(pwd)}"

lib_stem="${lib_name%.*}"
rm -f "${lib_dir}/${lib_stem}.h"

if [[ "${RUNNER_OS:-}" == "Windows" ]]; then
	powershell -Command "Compress-Archive -Path '${lib_dir}/${lib_name}' -DestinationPath '${archive_name}'"
	powershell -NoProfile -Command "(Get-FileHash -Algorithm SHA256 '${archive_name}').Hash.ToLower() + '  ${archive_name}'" > "${archive_name}.sha256"
else
	(
		cd "${lib_dir}"
		zip -r "${repo_root}/${archive_name}" "${lib_name}"
	)
	sha256sum "${archive_name}" > "${archive_name}.sha256"
fi
