#!/bin/sh
# Hermetic offline tests for scripts/release-zip.sh.
# Uses temporary workspaces with fake security/codesign/go/uname binaries and a
# stub third-party-notices.py: never accesses real keychains, never launches any
# GUI application and never touches the network. Real ditto/shasum are used so
# the zip round-trip is exercised end to end.
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd -P)
PKG_SCRIPT_SRC="$REPO_ROOT/scripts/package-macos.sh"
RELZIP_SCRIPT_SRC="$REPO_ROOT/scripts/release-zip.sh"
MYGO_JSON_SRC="$REPO_ROOT/mygo.json"

TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/branchline-relzip-test.XXXXXX")
cleanup() {
    rm -rf "$TEST_ROOT"
}
trap cleanup EXIT INT TERM

SHA_ENV='AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'
SHA_ENV_LOWER='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'

FAKE_BIN="$TEST_ROOT/fakebin"
mkdir -p "$FAKE_BIN"

cat >"$FAKE_BIN/security" <<'EOF'
#!/bin/sh
set -eu
if [ -n "${FAKE_LOG_DIR:-}" ]; then
    printf '%s\n' "$*" >>"$FAKE_LOG_DIR/security.log"
fi
cat <<'OUT'
  1) AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA "Branchline Env Cert"
  2) BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB "Branchline Local Cert"
  3) CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC "Branchline Main Cert"
     3 valid identities found
OUT
EOF
chmod +x "$FAKE_BIN/security"

cat >"$FAKE_BIN/go" <<'EOF'
#!/bin/sh
set -eu
# Mirrors the real toolchain: an explicit GOARCH environment variable wins
# over the default, which the test controls through FAKE_GOARCH.
if [ "${1:-}" = 'env' ] && [ "${2:-}" = 'GOARCH' ]; then
    printf '%s\n' "${GOARCH:-${FAKE_GOARCH:-arm64}}"
    exit 0
fi
echo "Unexpected fake go invocation: $*" >&2
exit 99
EOF
chmod +x "$FAKE_BIN/go"

cat >"$FAKE_BIN/uname" <<'EOF'
#!/bin/sh
set -eu
if [ "${1:-}" = '-s' ] && [ "$#" -eq 1 ]; then
    printf '%s\n' "${FAKE_UNAME_S:-Darwin}"
    exit 0
fi
exec /usr/bin/uname "$@"
EOF
chmod +x "$FAKE_BIN/uname"

cat >"$FAKE_BIN/codesign" <<'EOF'
#!/bin/sh
set -eu
if [ -n "${FAKE_LOG_DIR:-}" ]; then
    printf '%s\n' "$*" >>"$FAKE_LOG_DIR/codesign.log"
fi

case "${1:-}" in
    --verify)
        if [ "${FAKE_CODESIGN_VERIFY_FAIL:-0}" = '1' ]; then
            echo 'code object is not signed at all' >&2
            exit 1
        fi
        target=''
        for arg in "$@"; do
            target=$arg
        done
        case "$target" in
            *unpack*)
                if [ "${FAKE_ROUNDTRIP_VERIFY_FAIL:-0}" = '1' ]; then
                    echo 'code object is not signed at all (simulated round-trip damage)' >&2
                    exit 1
                fi
                ;;
        esac
        if [ -n "${FAKE_EXPECT_VERIFY_REQ:-}" ]; then
            found_req=0
            for arg in "$@"; do
                if [ "$arg" = "$FAKE_EXPECT_VERIFY_REQ" ]; then
                    found_req=1
                    break
                fi
            done
            if [ "$found_req" -ne 1 ]; then
                echo "Expected -R requirement '$FAKE_EXPECT_VERIFY_REQ', got: $*" >&2
                exit 1
            fi
        fi
        exit 0
        ;;
    -dv)
        {
            echo 'Executable=build/darwin-arm64/Branchline.app/Contents/MacOS/Branchline'
            printf 'Identifier=%s\n' "${FAKE_DV_IDENTIFIER:-com.zhdsmy.branchline}"
            echo 'Format=app bundle with Mach-O thin (arm64)'
            printf 'CodeDirectory v=20500 size=1234 flags=%s hashes=42+3 location=embedded\n' "${FAKE_DV_FLAGS:-0x10000(runtime)}"
            echo 'Signature size=1800'
            printf 'Authority=%s\n' "${FAKE_DV_AUTHORITY:-Branchline Test Development}"
            printf 'TeamIdentifier=%s\n' "${FAKE_DV_TEAM:-not set}"
        } >&2
        exit 0
        ;;
    -dr)
        echo 'Executable=build/darwin-arm64/Branchline.app/Contents/MacOS/Branchline' >&2
        printf '%s\n' "${FAKE_DR_LINE:-designated => identifier \"com.zhdsmy.branchline\" and certificate leaf = H\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"}"
        exit 0
        ;;
    *)
        echo "Unexpected fake codesign invocation: $*" >&2
        exit 99
        ;;
esac
EOF
chmod +x "$FAKE_BIN/codesign"

TPN_STUB="$TEST_ROOT/third-party-notices.py"
cat >"$TPN_STUB" <<'EOF'
"""Offline stub of the notices checker for hermetic packaging tests."""
import os
import sys

if len(sys.argv) != 2 or sys.argv[1] not in ("--check", "--write"):
    sys.stderr.write("stub: expected exactly one of --check/--write\n")
    sys.exit(2)
if os.environ.get("FAKE_TPN_FAIL") == "1":
    sys.stderr.write("third-party-notices: THIRD_PARTY_NOTICES is out of date (simulated)\n")
    sys.exit(1)
print("third-party-notices: THIRD_PARTY_NOTICES is up to date (stub)")
EOF

BASE_PATH="$FAKE_BIN:/usr/bin:/bin:/usr/sbin:/sbin"

PASS_COUNT=0
FAIL_COUNT=0

pass() {
    PASS_COUNT=$((PASS_COUNT + 1))
    printf 'PASS: %s\n' "$1"
}

fail() {
    FAIL_COUNT=$((FAIL_COUNT + 1))
    printf 'FAIL: %s\n' "$1" >&2
    if [ -n "${2:-}" ]; then
        printf '  detail: %s\n' "$2" >&2
    fi
}

assert_contains() {
    case "$1" in
        *"$2"*) return 0 ;;
        *) return 1 ;;
    esac
}

make_workspace() {
    ws="$TEST_ROOT/$1"
    mkdir -p "$ws/scripts" "$ws/logs" "$ws/tmp"
    cp "$PKG_SCRIPT_SRC" "$ws/scripts/package-macos.sh"
    cp "$RELZIP_SCRIPT_SRC" "$ws/scripts/release-zip.sh"
    cp "$TPN_STUB" "$ws/scripts/third-party-notices.py"
    cp "$MYGO_JSON_SRC" "$ws/mygo.json"
    cp "$REPO_ROOT/LICENSE" "$ws/LICENSE"
    cp "$REPO_ROOT/THIRD_PARTY_NOTICES" "$ws/THIRD_PARTY_NOTICES"
    printf '%s' "$ws"
}

make_valid_bundle() {
    ws=$1
    arch=${2:-arm64}
    app="$ws/build/darwin-$arch/Branchline.app"
    rm -rf "$app"
    mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
    printf '#!/bin/sh\nexit 0\n' >"$app/Contents/MacOS/Branchline"
    chmod +x "$app/Contents/MacOS/Branchline"
    ln -s Branchline "$app/Contents/MacOS/BranchlineAlias"
    printf 'icns-placeholder-data' >"$app/Contents/Resources/AppIcon.icns"
    printf 'car-placeholder-data' >"$app/Contents/Resources/Assets.car"
    cp "$ws/LICENSE" "$app/Contents/Resources/LICENSE"
    cp "$ws/THIRD_PARTY_NOTICES" "$app/Contents/Resources/THIRD_PARTY_NOTICES"
    cat >"$app/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDisplayName</key>
	<string>Branchline</string>
	<key>CFBundleExecutable</key>
	<string>Branchline</string>
	<key>CFBundleIconFile</key>
	<string>AppIcon.icns</string>
	<key>CFBundleIconName</key>
	<string>AppIcon</string>
	<key>CFBundleIdentifier</key>
	<string>com.zhdsmy.branchline</string>
	<key>CFBundleName</key>
	<string>Branchline</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>0.2.0</string>
	<key>CFBundleURLTypes</key>
	<array>
		<dict>
			<key>CFBundleURLName</key>
			<string>com.zhdsmy.branchline</string>
			<key>CFBundleURLSchemes</key>
			<array>
				<string>branchline</string>
			</array>
		</dict>
	</array>
	<key>CFBundleVersion</key>
	<string>0.2.0</string>
	<key>LSMinimumSystemVersion</key>
	<string>12.0</string>
</dict>
</plist>
PLIST
}

# Runs the packaging script expecting failure; asserts a non-zero exit and an
# expected message substring.
run_expect_fail() {
    name=$1
    expected_substr=$2
    ws=$3
    shift 3
    status=0
    out=$(env -i PATH="$BASE_PATH" HOME="$HOME" TMPDIR="$ws/tmp" FAKE_LOG_DIR="$ws/logs" "$@" 2>&1) || status=$?
    if [ "$status" -eq 0 ]; then
        fail "$name" "expected non-zero exit, got 0. Output: $out"
        return 1
    fi
    if [ -n "$expected_substr" ] && ! assert_contains "$out" "$expected_substr"; then
        fail "$name" "expected output containing '$expected_substr', got: $out"
        return 1
    fi
    printf '%s' "$out"
    return 0
}

# Asserts that no staging directories survive under the workspace tmp dir and
# that the output directory contains nothing beyond the pre-seeded files given
# as extra arguments.
assert_no_new_artifacts() {
    name=$1
    ws=$2
    outdir=$3
    shift 3
    residue_ok=1
    for d in "$ws/tmp"/branchline-release-zip.*; do
        if [ -e "$d" ]; then
            fail "$name" "staging directory was not cleaned up: $d"
            residue_ok=0
        fi
    done
    if [ -d "$outdir" ]; then
        for f in "$outdir"/* "$outdir"/.[!.]*; do
            [ -e "$f" ] || continue
            preseeded=0
            for p in "$@"; do
                [ "$f" = "$p" ] && preseeded=1
            done
            if [ "$preseeded" -eq 0 ]; then
                fail "$name" "unexpected artifact left behind: $f"
                residue_ok=0
            fi
        done
    fi
    [ "$residue_ok" -eq 1 ]
}

# Verifies a produced zip end to end: exact top-level entries, no junk entries
# anywhere, license byte-equality in all four positions, exec bit and symlink.
verify_zip_contents() {
    name=$1
    ws=$2
    zip_path=$3
    check_dir="$TEST_ROOT/check-$name"
    rm -rf "$check_dir"
    mkdir -p "$check_dir"
    zipcheck_ok=1

    zip_report=$(python3 - "$zip_path" <<'PY'
import sys, zipfile

path = sys.argv[1]
try:
    with zipfile.ZipFile(path) as zf:
        names = zf.namelist()
except Exception as exc:
    print(f"ERROR unreadable zip: {exc}")
    sys.exit(0)

top = set()
junk = []
for entry in names:
    parts = [p for p in entry.split("/") if p]
    if not parts:
        continue
    top.add(parts[0])
    for part in parts:
        if part == "__MACOSX" or part == ".DS_Store":
            junk.append(entry)
print("top:" + ",".join(sorted(top)))
print("junk:" + ",".join(junk))
print("count:" + str(len(names)))
PY
)
    zip_top=$(printf '%s\n' "$zip_report" | sed -n 's/^top://p')
    zip_junk=$(printf '%s\n' "$zip_report" | sed -n 's/^junk://p')
    if [ "$zip_top" != 'Branchline.app,LICENSE,THIRD_PARTY_NOTICES' ]; then
        fail "$name" "zip top-level entries wrong: $zip_top"
        zipcheck_ok=0
    fi
    if [ -n "$zip_junk" ]; then
        fail "$name" "zip contains junk entries: $zip_junk"
        zipcheck_ok=0
    fi

    ditto -x -k "$zip_path" "$check_dir" || {
        fail "$name" "independent extraction of $zip_path failed"
        return 1
    }
    for license_file in LICENSE THIRD_PARTY_NOTICES; do
        cmp -s "$ws/$license_file" "$check_dir/$license_file" || {
            fail "$name" "top-level $license_file in zip differs from repository copy"
            zipcheck_ok=0
        }
        cmp -s "$ws/$license_file" "$check_dir/Branchline.app/Contents/Resources/$license_file" || {
            fail "$name" "bundled Contents/Resources/$license_file in zip differs from repository copy"
            zipcheck_ok=0
        }
    done
    [ -x "$check_dir/Branchline.app/Contents/MacOS/Branchline" ] || {
        fail "$name" "bundle executable lost its executable bit in the zip"
        zipcheck_ok=0
    }
    [ -L "$check_dir/Branchline.app/Contents/MacOS/BranchlineAlias" ] || {
        fail "$name" "bundle symlink was not preserved in the zip"
        zipcheck_ok=0
    }
    [ "$zipcheck_ok" -eq 1 ]
}

# ---------------------------------------------------------------------------
# Positive: default invocation packages the signed bundle with the default
# version v<mygo.json version> and passes every round-trip assertion.
# ---------------------------------------------------------------------------
ws=$(make_workspace positive_default)
make_valid_bundle "$ws"
status=0
out=$(env -i PATH="$BASE_PATH" HOME="$HOME" TMPDIR="$ws/tmp" FAKE_LOG_DIR="$ws/logs" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" \
    FAKE_EXPECT_VERIFY_REQ="=certificate leaf = H\"$SHA_ENV\"" \
    sh "$ws/scripts/release-zip.sh" 2>&1) || status=$?

zip_path="$ws/build/release/Branchline-v0.2.0-darwin-arm64.zip"
sha_path="$zip_path.sha256"
pos_ok=1
if [ "$status" -ne 0 ]; then
    fail 'positive_default' "expected exit 0, got $status: $out"
    pos_ok=0
else
    assert_contains "$out" 'Release zip created:' || { fail 'positive_default' "summary missing header: $out"; pos_ok=0; }
    assert_contains "$out" 'Version: v0.2.0' || { fail 'positive_default' "summary missing version: $out"; pos_ok=0; }
    assert_contains "$out" 'Architecture: darwin-arm64' || { fail 'positive_default' "summary missing arch: $out"; pos_ok=0; }
    assert_contains "$out" 'SHA-256: ' || { fail 'positive_default' "summary missing digest: $out"; pos_ok=0; }
    assert_contains "$out" 'Contents: Branchline.app, LICENSE, THIRD_PARTY_NOTICES' || { fail 'positive_default' "summary missing contents: $out"; pos_ok=0; }
fi
[ -f "$zip_path" ] || { fail 'positive_default' 'zip artifact missing'; pos_ok=0; }
[ -f "$sha_path" ] || { fail 'positive_default' 'sha256 artifact missing'; pos_ok=0; }

if [ -f "$sha_path" ]; then
    sha_line=$(cat "$sha_path")
    printf '%s' "$sha_line" | grep -Eq '^[0-9a-f]{64}  Branchline-v0\.2\.0-darwin-arm64\.zip$' || {
        fail 'positive_default' "sha256 file format wrong: $sha_line"
        pos_ok=0
    }
    case "$sha_line" in
        *"$ws"*|*'/'*)
            fail 'positive_default' "sha256 file must not contain paths: $sha_line"
            pos_ok=0 ;;
    esac
    shasum_status=0
    shasum_out=$(cd "$ws/build/release" && shasum -a 256 -c 'Branchline-v0.2.0-darwin-arm64.zip.sha256' 2>&1) || shasum_status=$?
    if [ "$shasum_status" -ne 0 ]; then
        fail 'positive_default' "independent shasum -c failed: $shasum_out"
        pos_ok=0
    fi
fi

if [ -f "$zip_path" ]; then
    verify_zip_contents positive_default "$ws" "$zip_path" || pos_ok=0
fi

if [ -f "$ws/logs/codesign.log" ]; then
    roundtrip_lines=$(grep 'verify.*unpack' "$ws/logs/codesign.log" | grep -c "=certificate leaf = H\"$SHA_ENV\"" || true)
    if [ "$roundtrip_lines" -ne 1 ]; then
        fail 'positive_default' "expected exactly one round-trip codesign --verify with the identity requirement, got $roundtrip_lines"
        pos_ok=0
    fi
else
    fail 'positive_default' 'codesign log missing'
    pos_ok=0
fi

assert_no_new_artifacts positive_default "$ws" "$ws/build/release" "$zip_path" "$sha_path" || pos_ok=0
[ "$pos_ok" -eq 1 ] && pass 'default invocation packages zip+sha256 with correct naming, checksum format, round-trip signature check and clean staging'

# ---------------------------------------------------------------------------
# Positive: explicit --version with a pre-release tag.
# ---------------------------------------------------------------------------
ws=$(make_workspace positive_version)
make_valid_bundle "$ws"
status=0
out=$(env -i PATH="$BASE_PATH" HOME="$HOME" TMPDIR="$ws/tmp" FAKE_LOG_DIR="$ws/logs" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" \
    sh "$ws/scripts/release-zip.sh" --version v0.2.0-alpha.1 2>&1) || status=$?
if [ "$status" -eq 0 ] && [ -f "$ws/build/release/Branchline-v0.2.0-alpha.1-darwin-arm64.zip" ] \
    && [ -f "$ws/build/release/Branchline-v0.2.0-alpha.1-darwin-arm64.zip.sha256" ] \
    && assert_contains "$out" 'Version: v0.2.0-alpha.1'; then
    pass 'explicit --version v0.2.0-alpha.1 produces a pre-release tagged zip'
else
    fail 'positive_version' "status=$status out=$out"
fi

# ---------------------------------------------------------------------------
# Positive: a version without the leading v is normalized, via flag and via
# the environment variable.
# ---------------------------------------------------------------------------
ws=$(make_workspace positive_normalize)
make_valid_bundle "$ws"
status=0
env -i PATH="$BASE_PATH" HOME="$HOME" TMPDIR="$ws/tmp" FAKE_LOG_DIR="$ws/logs" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" \
    sh "$ws/scripts/release-zip.sh" --version 0.2.0-alpha.1 >/dev/null 2>&1 || status=$?
norm_ok=1
[ "$status" -eq 0 ] || { fail 'positive_normalize_flag' "exit $status"; norm_ok=0; }
[ -f "$ws/build/release/Branchline-v0.2.0-alpha.1-darwin-arm64.zip" ] || { fail 'positive_normalize_flag' 'zip missing or not normalized to leading v'; norm_ok=0; }

ws2=$(make_workspace positive_normalize_env)
make_valid_bundle "$ws2"
status=0
env -i PATH="$BASE_PATH" HOME="$HOME" TMPDIR="$ws2/tmp" FAKE_LOG_DIR="$ws2/logs" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" BRANCHLINE_RELEASE_VERSION=0.2.0-alpha.1 \
    sh "$ws2/scripts/release-zip.sh" >/dev/null 2>&1 || status=$?
[ "$status" -eq 0 ] || { fail 'positive_normalize_env' "exit $status"; norm_ok=0; }
[ -f "$ws2/build/release/Branchline-v0.2.0-alpha.1-darwin-arm64.zip" ] || { fail 'positive_normalize_env' 'zip missing or not normalized to leading v'; norm_ok=0; }
[ "$norm_ok" -eq 1 ] && pass 'version without leading v is normalized via --version flag and via environment variable'

# ---------------------------------------------------------------------------
# Positive: --arch amd64 selects the amd64 bundle and names artifacts for it.
# ---------------------------------------------------------------------------
ws=$(make_workspace positive_amd64)
make_valid_bundle "$ws" amd64
status=0
out=$(env -i PATH="$BASE_PATH" HOME="$HOME" TMPDIR="$ws/tmp" FAKE_LOG_DIR="$ws/logs" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" \
    sh "$ws/scripts/release-zip.sh" --arch amd64 2>&1) || status=$?
if [ "$status" -eq 0 ] && [ -f "$ws/build/release/Branchline-v0.2.0-darwin-amd64.zip" ] \
    && assert_contains "$out" 'Architecture: darwin-amd64'; then
    pass '--arch amd64 packages the amd64 bundle with matching artifact names'
else
    fail 'positive_amd64' "status=$status out=$out"
fi

ws=$(make_workspace positive_goarch)
make_valid_bundle "$ws" amd64
status=0
env -i PATH="$BASE_PATH" HOME="$HOME" TMPDIR="$ws/tmp" FAKE_LOG_DIR="$ws/logs" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" FAKE_GOARCH=amd64 \
    sh "$ws/scripts/release-zip.sh" >/dev/null 2>&1 || status=$?
if [ "$status" -eq 0 ] && [ -f "$ws/build/release/Branchline-v0.2.0-darwin-amd64.zip" ]; then
    pass 'default architecture comes from go env GOARCH'
else
    fail 'positive_goarch' "exit $status"
fi

# ---------------------------------------------------------------------------
# Positive: --out-dir redirect, relative and absolute.
# ---------------------------------------------------------------------------
ws=$(make_workspace positive_outdir)
make_valid_bundle "$ws"
status=0
env -i PATH="$BASE_PATH" HOME="$HOME" TMPDIR="$ws/tmp" FAKE_LOG_DIR="$ws/logs" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" \
    sh "$ws/scripts/release-zip.sh" --out-dir dist >/dev/null 2>&1 || status=$?
outdir_ok=1
[ "$status" -eq 0 ] || { fail 'positive_outdir_rel' "exit $status"; outdir_ok=0; }
[ -f "$ws/dist/Branchline-v0.2.0-darwin-arm64.zip" ] || { fail 'positive_outdir_rel' 'zip missing in relative out dir'; outdir_ok=0; }

abs_out="$ws/abs-out"
status=0
env -i PATH="$BASE_PATH" HOME="$HOME" TMPDIR="$ws/tmp" FAKE_LOG_DIR="$ws/logs" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" \
    sh "$ws/scripts/release-zip.sh" --force --out-dir "$abs_out" >/dev/null 2>&1 || status=$?
[ "$status" -eq 0 ] || { fail 'positive_outdir_abs' "exit $status"; outdir_ok=0; }
[ -f "$abs_out/Branchline-v0.2.0-darwin-arm64.zip" ] || { fail 'positive_outdir_abs' 'zip missing in absolute out dir'; outdir_ok=0; }
[ "$outdir_ok" -eq 1 ] && pass '--out-dir redirects artifacts to relative and absolute directories'

# ---------------------------------------------------------------------------
# Positive: --force replaces pre-existing artifacts after full verification.
# ---------------------------------------------------------------------------
ws=$(make_workspace positive_force)
make_valid_bundle "$ws"
mkdir -p "$ws/build/release"
printf 'sentinel-zip\n' >"$ws/build/release/Branchline-v0.2.0-darwin-arm64.zip"
printf 'sentinel-sha\n' >"$ws/build/release/Branchline-v0.2.0-darwin-arm64.zip.sha256"
status=0
env -i PATH="$BASE_PATH" HOME="$HOME" TMPDIR="$ws/tmp" FAKE_LOG_DIR="$ws/logs" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" \
    sh "$ws/scripts/release-zip.sh" --force >/dev/null 2>&1 || status=$?
force_ok=1
[ "$status" -eq 0 ] || { fail 'positive_force' "exit $status"; force_ok=0; }
if printf 'sentinel-zip\n' | cmp -s - "$ws/build/release/Branchline-v0.2.0-darwin-arm64.zip"; then
    fail 'positive_force' 'zip still contains the sentinel content'
    force_ok=0
fi
if printf 'sentinel-sha\n' | cmp -s - "$ws/build/release/Branchline-v0.2.0-darwin-arm64.zip.sha256"; then
    fail 'positive_force' 'sha256 still contains the sentinel content'
    force_ok=0
fi
if [ "$force_ok" -eq 1 ]; then
    verify_zip_contents positive_force "$ws" "$ws/build/release/Branchline-v0.2.0-darwin-arm64.zip" || force_ok=0
fi
[ "$force_ok" -eq 1 ] && pass '--force replaces existing artifacts with fully verified new ones'

# ---------------------------------------------------------------------------
# Negative: missing target bundle fails before any packaging.
# ---------------------------------------------------------------------------
ws=$(make_workspace negative_missing_app)
if run_expect_fail 'missing_app' 'Target app bundle not found' "$ws" \
        BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/release-zip.sh" >/dev/null \
    && assert_no_new_artifacts missing_app "$ws" "$ws/build/release"; then
    pass 'missing .app bundle fails closed with no residue'
fi

# ---------------------------------------------------------------------------
# Negative: signature verification failure refuses packaging.
# ---------------------------------------------------------------------------
ws=$(make_workspace negative_verify_fail)
make_valid_bundle "$ws"
if run_expect_fail 'verify_fail' 'refusing to package' "$ws" \
        BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" FAKE_CODESIGN_VERIFY_FAIL=1 sh "$ws/scripts/release-zip.sh" >/dev/null \
    && assert_no_new_artifacts verify_fail "$ws" "$ws/build/release"; then
    pass 'package-macos.sh verify failure refuses packaging with no residue'
fi

# ---------------------------------------------------------------------------
# Negative: stale third-party notices refuse packaging.
# ---------------------------------------------------------------------------
ws=$(make_workspace negative_notices_stale)
make_valid_bundle "$ws"
if run_expect_fail 'notices_stale' 'out of date' "$ws" \
        BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" FAKE_TPN_FAIL=1 sh "$ws/scripts/release-zip.sh" >/dev/null \
    && assert_no_new_artifacts notices_stale "$ws" "$ws/build/release"; then
    pass 'stale THIRD_PARTY_NOTICES refuses packaging with no residue'
fi

# ---------------------------------------------------------------------------
# Negative: a tampered in-bundle notices file is caught by pre-verification.
# ---------------------------------------------------------------------------
ws=$(make_workspace negative_notices_tampered)
make_valid_bundle "$ws"
printf 'tampered notices\n' >"$ws/build/darwin-arm64/Branchline.app/Contents/Resources/THIRD_PARTY_NOTICES"
if run_expect_fail 'notices_tampered' '' "$ws" \
        BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/release-zip.sh" >/dev/null \
    && assert_no_new_artifacts notices_tampered "$ws" "$ws/build/release"; then
    pass 'tampered bundle THIRD_PARTY_NOTICES fails packaging with no residue'
fi

# ---------------------------------------------------------------------------
# Negative: extraction round-trip signature failure refuses publishing.
# ---------------------------------------------------------------------------
ws=$(make_workspace negative_roundtrip)
make_valid_bundle "$ws"
if run_expect_fail 'roundtrip_verify_fail' 'did not survive packaging' "$ws" \
        BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" FAKE_ROUNDTRIP_VERIFY_FAIL=1 sh "$ws/scripts/release-zip.sh" >/dev/null \
    && assert_no_new_artifacts roundtrip_verify_fail "$ws" "$ws/build/release"; then
    pass 'round-trip codesign --verify failure refuses publishing with no residue'
fi

# ---------------------------------------------------------------------------
# Negative: version core must match mygo.json.
# ---------------------------------------------------------------------------
ws=$(make_workspace negative_version_mismatch)
make_valid_bundle "$ws"
mismatch_ok=1
run_expect_fail 'version_minor_mismatch' 'does not match mygo.json version' "$ws" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/release-zip.sh" --version v0.3.0-alpha.1 >/dev/null || mismatch_ok=0
run_expect_fail 'version_patch_mismatch' 'does not match mygo.json version' "$ws" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/release-zip.sh" --version 0.2.1 >/dev/null || mismatch_ok=0
run_expect_fail 'version_env_mismatch' 'does not match mygo.json version' "$ws" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" BRANCHLINE_RELEASE_VERSION=v1.0.0 sh "$ws/scripts/release-zip.sh" >/dev/null || mismatch_ok=0
assert_no_new_artifacts version_mismatch "$ws" "$ws/build/release" || mismatch_ok=0
[ "$mismatch_ok" -eq 1 ] && pass 'version core mismatching mygo.json is rejected'

# ---------------------------------------------------------------------------
# Negative: malformed or path-traversing versions are rejected.
# ---------------------------------------------------------------------------
ws=$(make_workspace negative_version_invalid)
make_valid_bundle "$ws"
invalid_ok=1
for bad_version in '../evil' 'v1.2' 'v1.2.3/..' 'v0.2.0 alpha' 'v0.2.0..x' 'v0.2.0/alpha' 'evil'; do
    run_expect_fail "invalid_version" 'Invalid release version' "$ws" \
        BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/release-zip.sh" --version "$bad_version" >/dev/null || invalid_ok=0
done
run_expect_fail 'empty_version_flag' 'Empty release version' "$ws" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/release-zip.sh" --version '' >/dev/null || invalid_ok=0
assert_no_new_artifacts version_invalid "$ws" "$ws/build/release" || invalid_ok=0
[ "$invalid_ok" -eq 1 ] && pass 'malformed, empty and path-traversing versions are rejected'

# ---------------------------------------------------------------------------
# Negative: existing artifacts are never overwritten without --force, and the
# refusal happens before any verification or packaging work.
# ---------------------------------------------------------------------------
ws=$(make_workspace negative_overwrite_zip)
make_valid_bundle "$ws"
mkdir -p "$ws/build/release"
printf 'sentinel-zip\n' >"$ws/build/release/Branchline-v0.2.0-darwin-arm64.zip"
printf 'sentinel-zip\n' >"$TEST_ROOT/sentinel-zip-copy"
zip_guard_ok=1
run_expect_fail 'overwrite_zip_refused' 'Refusing to overwrite' "$ws" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/release-zip.sh" >/dev/null || zip_guard_ok=0
cmp -s "$TEST_ROOT/sentinel-zip-copy" "$ws/build/release/Branchline-v0.2.0-darwin-arm64.zip" || {
    fail 'overwrite_zip_refused' 'pre-existing zip content changed despite refusal'
    zip_guard_ok=0
}
assert_no_new_artifacts overwrite_zip "$ws" "$ws/build/release" "$ws/build/release/Branchline-v0.2.0-darwin-arm64.zip" || zip_guard_ok=0

ws=$(make_workspace negative_overwrite_sha)
make_valid_bundle "$ws"
mkdir -p "$ws/build/release"
printf 'sentinel-sha\n' >"$ws/build/release/Branchline-v0.2.0-darwin-arm64.zip.sha256"
printf 'sentinel-sha\n' >"$TEST_ROOT/sentinel-sha-copy"
run_expect_fail 'overwrite_sha_refused' 'Refusing to overwrite' "$ws" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/release-zip.sh" >/dev/null || zip_guard_ok=0
cmp -s "$TEST_ROOT/sentinel-sha-copy" "$ws/build/release/Branchline-v0.2.0-darwin-arm64.zip.sha256" || {
    fail 'overwrite_sha_refused' 'pre-existing sha256 content changed despite refusal'
    zip_guard_ok=0
}
assert_no_new_artifacts overwrite_sha "$ws" "$ws/build/release" "$ws/build/release/Branchline-v0.2.0-darwin-arm64.zip.sha256" || zip_guard_ok=0
[ "$zip_guard_ok" -eq 1 ] && pass 'pre-existing zip or sha256 without --force is refused and preserved byte-for-byte'

# ---------------------------------------------------------------------------
# Negative: overwrite protection also holds when packaging would fail later.
# ---------------------------------------------------------------------------
ws=$(make_workspace negative_force_keeps_old)
make_valid_bundle "$ws"
mkdir -p "$ws/build/release"
printf 'sentinel-zip\n' >"$ws/build/release/Branchline-v0.2.0-darwin-arm64.zip"
printf 'sentinel-sha\n' >"$ws/build/release/Branchline-v0.2.0-darwin-arm64.zip.sha256"
printf 'sentinel-zip\n' >"$TEST_ROOT/fkeep-zip-copy"
printf 'sentinel-sha\n' >"$TEST_ROOT/fkeep-sha-copy"
force_fail_ok=1
run_expect_fail 'force_roundtrip_fail' 'did not survive packaging' "$ws" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" FAKE_ROUNDTRIP_VERIFY_FAIL=1 sh "$ws/scripts/release-zip.sh" --force >/dev/null || force_fail_ok=0
cmp -s "$TEST_ROOT/fkeep-zip-copy" "$ws/build/release/Branchline-v0.2.0-darwin-arm64.zip" || {
    fail 'force_roundtrip_fail' 'pre-existing zip changed even though packaging failed'
    force_fail_ok=0
}
cmp -s "$TEST_ROOT/fkeep-sha-copy" "$ws/build/release/Branchline-v0.2.0-darwin-arm64.zip.sha256" || {
    fail 'force_roundtrip_fail' 'pre-existing sha256 changed even though packaging failed'
    force_fail_ok=0
}
assert_no_new_artifacts force_roundtrip_fail "$ws" "$ws/build/release" \
    "$ws/build/release/Branchline-v0.2.0-darwin-arm64.zip" \
    "$ws/build/release/Branchline-v0.2.0-darwin-arm64.zip.sha256" || force_fail_ok=0
[ "$force_fail_ok" -eq 1 ] && pass 'failed packaging with --force leaves pre-existing artifacts byte-for-byte intact'

# ---------------------------------------------------------------------------
# Negative: unsupported or empty architectures are rejected.
# ---------------------------------------------------------------------------
ws=$(make_workspace negative_arch)
make_valid_bundle "$ws"
arch_ok=1
run_expect_fail 'arch_unsupported' 'Unsupported macOS architecture' "$ws" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/release-zip.sh" --arch armv7 >/dev/null || arch_ok=0
run_expect_fail 'arch_empty' 'Empty --arch value' "$ws" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/release-zip.sh" --arch '' >/dev/null || arch_ok=0
assert_no_new_artifacts arch_reject "$ws" "$ws/build/release" || arch_ok=0
[ "$arch_ok" -eq 1 ] && pass 'unsupported and empty --arch values are rejected'

# ---------------------------------------------------------------------------
# Negative: non-macOS platforms are rejected.
# ---------------------------------------------------------------------------
ws=$(make_workspace negative_platform)
make_valid_bundle "$ws"
if run_expect_fail 'non_darwin' 'requires macOS' "$ws" \
        BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" FAKE_UNAME_S=Linux sh "$ws/scripts/release-zip.sh" >/dev/null \
    && assert_no_new_artifacts non_darwin "$ws" "$ws/build/release"; then
    pass 'non-macOS platform is rejected'
fi

# ---------------------------------------------------------------------------
# Negative: usage errors exit with status 2.
# ---------------------------------------------------------------------------
ws=$(make_workspace negative_usage)
usage_ok=1
for bad_args in '--bogus' '--version' '--arch' '--out-dir'; do
    status=0
    env -i PATH="$BASE_PATH" HOME="$HOME" TMPDIR="$ws/tmp" FAKE_LOG_DIR="$ws/logs" \
        BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/release-zip.sh" $bad_args >/dev/null 2>&1 || status=$?
    if [ "$status" -ne 2 ]; then
        fail 'usage' "expected exit 2 for '$bad_args', got $status"
        usage_ok=0
    fi
done
[ "$usage_ok" -eq 1 ] && pass 'usage errors exit with status 2'

printf '\nSummary: %d passed, %d failed\n' "$PASS_COUNT" "$FAIL_COUNT"
[ "$FAIL_COUNT" -eq 0 ]
