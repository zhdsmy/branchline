#!/bin/sh
# Package a signed Branchline.app into a release zip plus a .sha256 checksum file.
# Fail-closed: refuses to package anything that has not passed the full
# package-macos.sh verify pipeline and a fresh third-party notices check, and
# re-verifies the code signature after an extraction round-trip before publishing.
# Never overwrites existing artifacts without --force, never touches keychains,
# never uploads anything.
set -eu

usage() {
    cat >&2 <<'EOF'
Usage: sh scripts/release-zip.sh [--version <ver>] [--arch <arm64|amd64>] [--out-dir <dir>] [--force]

Packages the signed app bundle into <name>-<version>-darwin-<arch>.zip plus a
matching .zip.sha256 checksum file (default output directory: build/release).

Options:
  --version <ver>   release version tag, e.g. v0.2.0-alpha.1 or 0.2.0-alpha.1
                    (normalized to a leading-v tag; the core major.minor.patch
                    must match the version in mygo.json; default: v<mygo.json version>;
                    may also be set via BRANCHLINE_RELEASE_VERSION)
  --arch <arch>     target architecture arm64 or amd64 (default: go env GOARCH)
  --out-dir <dir>   output directory (default: build/release, created if missing)
  --force           replace existing artifacts after all verification passes
EOF
    exit 2
}

fail() {
    printf 'release-zip: %s\n' "$1" >&2
    exit 1
}

[ "$(uname -s)" = Darwin ] || fail 'This entry point requires macOS.'

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd -P)
cd "$REPO_ROOT"

CLI_VERSION=''
CLI_VERSION_SET=0
ARCH_ARG=''
ARCH_SET=0
OUT_DIR_ARG=''
OUT_DIR_SET=0
FORCE=0

while [ "$#" -gt 0 ]; do
    case "$1" in
        --version)
            [ "$#" -ge 2 ] || usage
            CLI_VERSION=$2
            CLI_VERSION_SET=1
            shift 2 ;;
        --arch)
            [ "$#" -ge 2 ] || usage
            ARCH_ARG=$2
            ARCH_SET=1
            shift 2 ;;
        --out-dir)
            [ "$#" -ge 2 ] || usage
            OUT_DIR_ARG=$2
            OUT_DIR_SET=1
            shift 2 ;;
        --force)
            FORCE=1
            shift ;;
        -h|--help)
            usage ;;
        *)
            usage ;;
    esac
done

# --- Single source of truth: mygo.json --------------------------------------

[ -f "$REPO_ROOT/mygo.json" ] || fail "Missing mygo.json in $REPO_ROOT"

cfg_lines=$(python3 - "$REPO_ROOT/mygo.json" <<'PY'
import json, sys

path = sys.argv[1]
try:
    with open(path, "r", encoding="utf-8") as f:
        cfg = json.load(f)
except Exception as exc:
    sys.stderr.write(f"Failed to parse {path}: {exc}\n")
    sys.exit(1)

if not isinstance(cfg, dict):
    sys.stderr.write(f"Invalid {path}: expected top-level JSON object\n")
    sys.exit(1)

def req_str(key):
    val = cfg.get(key)
    if not isinstance(val, str) or not val.strip():
        sys.stderr.write(f"Invalid {path}: '{key}' must be a non-empty string\n")
        sys.exit(1)
    if "\n" in val or "\r" in val:
        sys.stderr.write(f"Invalid {path}: '{key}' must not contain newlines\n")
        sys.exit(1)
    return val

name = req_str("name")
version = req_str("version")
out_dir = cfg.get("out") or "build"
if not isinstance(out_dir, str) or not out_dir.strip() or "\n" in out_dir or "\r" in out_dir:
    sys.stderr.write(f"Invalid {path}: 'out' must be a single-line string\n")
    sys.exit(1)

print(name)
print(version)
print(out_dir)
PY
) || fail 'Failed to read mygo.json configuration.'

APP_NAME=$(printf '%s\n' "$cfg_lines" | sed -n '1p')
APP_VERSION=$(printf '%s\n' "$cfg_lines" | sed -n '2p')
APP_OUT=$(printf '%s\n' "$cfg_lines" | sed -n '3p')

# --- Target architecture -----------------------------------------------------

if [ "$ARCH_SET" -eq 1 ]; then
    [ -n "$ARCH_ARG" ] || fail 'Empty --arch value: pass arm64 or amd64.'
    ARCH=$ARCH_ARG
else
    ARCH=$(go env GOARCH)
fi
case "$ARCH" in
    arm64|amd64) ;;
    *) fail "Unsupported macOS architecture: $ARCH (expected arm64 or amd64)" ;;
esac

APP_PATH="$APP_OUT/darwin-$ARCH/$APP_NAME.app"

# --- Release version resolution and validation -------------------------------

raw_version=''
if [ "$CLI_VERSION_SET" -eq 1 ]; then
    raw_version=$CLI_VERSION
elif [ "${BRANCHLINE_RELEASE_VERSION+set}" = set ]; then
    raw_version=$BRANCHLINE_RELEASE_VERSION
fi

if [ -z "$raw_version" ]; then
    if [ "$CLI_VERSION_SET" -eq 1 ] || [ "${BRANCHLINE_RELEASE_VERSION+set}" = set ]; then
        fail 'Empty release version: pass a non-empty value to --version or BRANCHLINE_RELEASE_VERSION.'
    fi
    raw_version="v$APP_VERSION"
fi

case "$raw_version" in
    v*) VERSION_TAG=$raw_version ;;
    *) VERSION_TAG="v$raw_version" ;;
esac

case "$VERSION_TAG" in
    *'..'*|*'/'*|*' '*)
        fail "Invalid release version '$raw_version': path separators, dot-dot sequences and whitespace are forbidden." ;;
esac

printf '%s' "$VERSION_TAG" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?$' || \
    fail "Invalid release version '$raw_version': expected v<major>.<minor>.<patch> with an optional alphanumeric pre-release suffix."

VERSION_CORE=$(printf '%s' "$VERSION_TAG" | cut -c2- | grep -o '^[0-9]*\.[0-9]*\.[0-9]*')
if [ "$VERSION_CORE" != "$APP_VERSION" ]; then
    fail "Release version core '$VERSION_CORE' does not match mygo.json version '$APP_VERSION' (refusing to package a $APP_VERSION build as $VERSION_TAG)."
fi

# --- Output directory and overwrite protection -------------------------------

OUT_DIR=build/release
if [ "$OUT_DIR_SET" -eq 1 ]; then
    [ -n "$OUT_DIR_ARG" ] || fail 'Empty --out-dir value: pass a directory path.'
    OUT_DIR=$OUT_DIR_ARG
fi
mkdir -p "$OUT_DIR" || fail "Failed to create output directory: $OUT_DIR"
OUT_DIR_ABS=$(CDPATH= cd -- "$OUT_DIR" && pwd -P)

ZIP_NAME="$APP_NAME-$VERSION_TAG-darwin-$ARCH.zip"
ZIP_PATH="$OUT_DIR_ABS/$ZIP_NAME"
SHA_PATH="$OUT_DIR_ABS/$ZIP_NAME.sha256"

if [ "$FORCE" -ne 1 ]; then
    for existing in "$ZIP_PATH" "$SHA_PATH"; do
        if [ -e "$existing" ]; then
            fail "Refusing to overwrite existing artifact: $existing (re-run with --force after verifying you want to replace it)"
        fi
    done
fi

# --- Precondition verification (fail-closed) ---------------------------------
# package-macos.sh derives the target bundle from `go env GOARCH`; exporting the
# resolved architecture pins check/verify to the exact bundle being packaged.

[ -d "$APP_PATH" ] || fail "Target app bundle not found: $APP_PATH (build and sign it first)"

if ! check_out=$(GOARCH=$ARCH sh "$SCRIPT_DIR/package-macos.sh" check); then
    fail 'Signing identity check failed (package-macos.sh check); see output above.'
fi
IDENTITY=$(printf '%s\n' "$check_out" | awk '/^Code-signing identity available: / { print $4; exit }')
case "$IDENTITY" in
    ''|*[!0-9a-fA-F]*)
        fail 'Failed to resolve signing identity from package-macos.sh check output.' ;;
esac
[ "${#IDENTITY}" -eq 40 ] || fail 'Failed to resolve signing identity from package-macos.sh check output.'

if ! GOARCH=$ARCH sh "$SCRIPT_DIR/package-macos.sh" verify; then
    fail 'Signature and bundle verification failed (package-macos.sh verify); refusing to package.'
fi

if ! python3 scripts/third-party-notices.py --check; then
    fail 'THIRD_PARTY_NOTICES is out of date; regenerate it before packaging a release.'
fi

# --- Staging, archiving and extraction round-trip self-check -----------------

WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/branchline-release-zip.XXXXXX")
cleanup() {
    rm -rf "$WORK_DIR"
}
trap cleanup EXIT INT TERM HUP

STAGE_DIR="$WORK_DIR/stage"
UNPACK_DIR="$WORK_DIR/unpack"
TMP_ZIP="$WORK_DIR/.release.zip.tmp"
TMP_SHA="$WORK_DIR/.release.sha256.tmp"
mkdir -p "$STAGE_DIR" "$UNPACK_DIR"

ditto --norsrc --noextattr --noqtn --noacl "$APP_PATH" "$STAGE_DIR/$APP_NAME.app" || \
    fail "Failed to copy $APP_PATH into the staging area."
cp "$REPO_ROOT/LICENSE" "$STAGE_DIR/LICENSE" || fail 'Failed to stage LICENSE.'
cp "$REPO_ROOT/THIRD_PARTY_NOTICES" "$STAGE_DIR/THIRD_PARTY_NOTICES" || fail 'Failed to stage THIRD_PARTY_NOTICES.'

ditto -c -k --norsrc --noextattr --noqtn "$STAGE_DIR" "$TMP_ZIP" || \
    fail 'Failed to create the release zip archive.'
[ -s "$TMP_ZIP" ] || fail 'Created zip archive is empty or missing.'

ditto -x -k "$TMP_ZIP" "$UNPACK_DIR" || \
    fail 'Extraction round-trip failed: could not extract the zip we just created.'

expected_entries=$(printf '%s\n' "$APP_NAME.app" LICENSE THIRD_PARTY_NOTICES | sort)
actual_entries=$(cd "$UNPACK_DIR" && ls -A | sort)
if [ "$actual_entries" != "$expected_entries" ]; then
    fail "Extraction round-trip failed: zip top level must contain exactly $APP_NAME.app, LICENSE and THIRD_PARTY_NOTICES; found: $(printf '%s' "$actual_entries" | tr '\n' ' ')"
fi

junk_found=$(find "$UNPACK_DIR" \( -name '__MACOSX' -o -name '.DS_Store' \) -print 2>/dev/null | head -1)
[ -z "$junk_found" ] || \
    fail "Extraction round-trip failed: archive contains junk entry $junk_found"

for license_file in LICENSE THIRD_PARTY_NOTICES; do
    cmp -s "$REPO_ROOT/$license_file" "$UNPACK_DIR/$license_file" || \
        fail "Extraction round-trip failed: top-level $license_file differs from the repository copy."
    cmp -s "$REPO_ROOT/$license_file" "$UNPACK_DIR/$APP_NAME.app/Contents/Resources/$license_file" || \
        fail "Extraction round-trip failed: bundled Contents/Resources/$license_file differs from the repository copy."
done

roundtrip_status=0
roundtrip_out=$(codesign --verify --deep --strict -R "=certificate leaf = H\"$IDENTITY\"" "$UNPACK_DIR/$APP_NAME.app" 2>&1) || roundtrip_status=$?
if [ "$roundtrip_status" -ne 0 ]; then
    echo "release-zip: extraction round-trip failed: code signature did not survive packaging (exit $roundtrip_status):" >&2
    [ -n "$roundtrip_out" ] && printf '%s\n' "$roundtrip_out" >&2
    exit 1
fi

# --- Checksum, atomic publish and self-verification --------------------------

zip_sha=$(shasum -a 256 "$TMP_ZIP" | awk '{print $1}')
case "$zip_sha" in
    *[!0-9a-f]*|'')
        fail 'Failed to compute a valid SHA-256 digest for the zip archive.' ;;
esac
[ "${#zip_sha}" -eq 64 ] || fail 'Failed to compute a valid SHA-256 digest for the zip archive.'
printf '%s  %s\n' "$zip_sha" "$ZIP_NAME" >"$TMP_SHA"

mv -f "$TMP_ZIP" "$ZIP_PATH" || fail "Failed to move the zip into place: $ZIP_PATH"
mv -f "$TMP_SHA" "$SHA_PATH" || fail "Failed to move the checksum file into place: $SHA_PATH"

shasum_status=0
shasum_out=$(cd "$OUT_DIR_ABS" && shasum -a 256 -c "$ZIP_NAME.sha256" 2>&1) || shasum_status=$?
if [ "$shasum_status" -ne 0 ]; then
    echo 'release-zip: final checksum self-verification failed:' >&2
    [ -n "$shasum_out" ] && printf '%s\n' "$shasum_out" >&2
    exit 1
fi

zip_bytes=$(stat -f %z "$ZIP_PATH")

printf 'Release zip created: %s\n' "$ZIP_PATH"
printf '  Version: %s\n' "$VERSION_TAG"
printf '  Architecture: darwin-%s\n' "$ARCH"
printf '  Size: %s bytes\n' "$zip_bytes"
printf '  SHA-256: %s\n' "$zip_sha"
printf '  Checksum file: %s\n' "$SHA_PATH"
printf '  Checksum self-verify: %s\n' "$shasum_out"
printf '  Contents: %s.app, LICENSE, THIRD_PARTY_NOTICES\n' "$APP_NAME"
