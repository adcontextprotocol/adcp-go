#!/usr/bin/env bash
# Print the targeting module's package paths (relative, space-separated) for
# `go test` and golangci-lint, leaving out targeting/glidestore when the Go
# toolchain refuses to build it.
#
# valkey-glide ships libglide_ffi.a prebuilt with LLVM bitcode (.llvmbc
# sections) inside, and Go 1.26.11, 1.27.4 and later reject linker inputs that
# carry LTO sections (golang/go#82572, CVE-2026-97038). CGO_LDFLAGS_ALLOW and
# the other environment settings do not lift that check, and valkey-glide
# v2.3.1, v2.5.0, v2.5.3 and v2.6.0 all trip it. Only that one error skips
# the package: any build failure without that error keeps it in the list, so
# the caller reports it. A glidestore compile error that comes with the LTO
# error stays hidden, because the LTO failure hits valkey-glide's options
# package first; Go 1.25 has no fix for the CVE and still builds glidestore,
# so the CI matrix's 1.25 job catches it.
#
# Run it from anywhere inside the module; drop the skip once a valkey-glide
# release no longer trips the check.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../../targeting"

pkgs=$(go list ./... | sed 's#^github.com/adcontextprotocol/adcp-go/targeting#.#')

build_output=$(go build ./glidestore 2>&1 || true)
if grep -q 'valkey-glide.*disallowed LTO section' <<<"$build_output"; then
  echo "::warning::$(go version | cut -d' ' -f3) rejects valkey-glide's prebuilt libglide_ffi.a (LTO section); skipping targeting/glidestore" >&2
  pkgs=$(grep -vx './glidestore' <<<"$pkgs" || true)
fi

[ -n "$pkgs" ] || { echo "no targeting packages found" >&2; exit 1; }

# shellcheck disable=SC2086 # one line of space-separated paths is the output
echo $pkgs
