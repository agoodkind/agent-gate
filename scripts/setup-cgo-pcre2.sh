#!/usr/bin/env bash
# Provision pcre2 for the build target named by GO_MK_TARGET_GOOS/GOARCH.
#
# agent-gate links pcre2 through cgo (#cgo pkg-config: libpcre2-8). On a linux
# target the system libpcre2-8 from apt (apt_packages: libpcre2-dev) satisfies
# that, so this script is a no-op. On the darwin cross target it builds a static
# pcre2 into GO_MK_CGO_PREFIX and drops a pkg-config .pc there, so the release
# binary links pcre2 statically and stays self-contained with no dylib to load.
#
# go-makefile's release command invokes this through the go-mk-cgo-dep-pcre2
# target with CC/CXX (the osxcross cross compiler), GO_MK_TARGET_GOOS/GOARCH, and
# GO_MK_CGO_PREFIX set, and PKG_CONFIG_PATH already pointed at the prefix.
set -euo pipefail

readonly PCRE2_VERSION="10.44"
readonly PCRE2_SHA256="86b9cb0aa3bcb7994faa88018292bc704cdbb708e785f7c74352ff6ea7d3175b"

target_goos="${GO_MK_TARGET_GOOS:-}"
target_goarch="${GO_MK_TARGET_GOARCH:-}"
prefix="${GO_MK_CGO_PREFIX:?GO_MK_CGO_PREFIX must be set by go-makefile}"

# Only darwin needs a provisioned build; linux uses the system pcre2 from apt.
if [[ "${target_goos}" != "darwin" ]]; then
    echo "setup-cgo-pcre2: ${target_goos:-host}/${target_goarch:-?} uses the system pcre2; nothing to build"
    exit 0
fi

cc="${CC:?CC must be set for the darwin cross build}"

# Reuse a prior build when the static lib and its .pc are already installed.
if [[ -f "${prefix}/lib/pkgconfig/libpcre2-8.pc" && -f "${prefix}/lib/libpcre2-8.a" ]]; then
    echo "setup-cgo-pcre2: static pcre2 ${PCRE2_VERSION} already present in ${prefix}"
    exit 0
fi

work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT INT TERM

tarball="${work_dir}/pcre2-${PCRE2_VERSION}.tar.gz"
url="https://github.com/PCRE2Project/pcre2/releases/download/pcre2-${PCRE2_VERSION}/pcre2-${PCRE2_VERSION}.tar.gz"

echo "setup-cgo-pcre2: downloading pcre2 ${PCRE2_VERSION}"
curl -fsSL -o "${tarball}" "${url}"

actual_sha256=""
if command -v sha256sum >/dev/null 2>&1; then
    actual_sha256="$(sha256sum "${tarball}" | awk '{print $1}')"
else
    actual_sha256="$(shasum -a 256 "${tarball}" | awk '{print $1}')"
fi
if [[ "${actual_sha256}" != "${PCRE2_SHA256}" ]]; then
    echo "setup-cgo-pcre2: checksum mismatch for pcre2 ${PCRE2_VERSION}" >&2
    echo "  expected ${PCRE2_SHA256}" >&2
    echo "  actual   ${actual_sha256}" >&2
    exit 1
fi

tar -xzf "${tarball}" -C "${work_dir}"
src_dir="${work_dir}/pcre2-${PCRE2_VERSION}"

# The osxcross clang wrapper in CC encodes the darwin target; -dumpmachine yields
# the matching triple, and osxcross ships <triple>-ar / <triple>-ranlib so
# configure can cross-build and archive a static library.
triple="$("${cc}" -dumpmachine)"

cd "${src_dir}"
./configure \
    --host="${triple}" \
    --prefix="${prefix}" \
    --disable-shared --enable-static \
    CC="${cc}" \
    AR="${triple}-ar" \
    RANLIB="${triple}-ranlib" \
    >/dev/null

make -j"$(getconf _NPROCESSORS_ONLN)" >/dev/null
make install >/dev/null

echo "setup-cgo-pcre2: installed static pcre2 ${PCRE2_VERSION} into ${prefix}"
