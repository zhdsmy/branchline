#!/bin/sh
# Hermetic tests for scripts/package-macos.sh and scripts/macos.sh delegation.
# Uses temporary workspaces and fake security/codesign/go stubs: never accesses
# real keychains and never launches any GUI application.
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd -P)
PKG_SCRIPT_SRC="$REPO_ROOT/scripts/package-macos.sh"
MACOS_SCRIPT_SRC="$REPO_ROOT/scripts/macos.sh"
MYGO_JSON_SRC="$REPO_ROOT/mygo.json"

TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/branchline-pkg-test.XXXXXX")
cleanup() {
    rm -rf "$TEST_ROOT"
}
trap cleanup EXIT INT TERM

SHA_ENV='AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'
SHA_ENV_LOWER='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
SHA_LOCAL='BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB'
SHA_LOCAL_LOWER='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
SHA_MAIN='CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC'
SHA_MISSING='DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD'

FAKE_BIN="$TEST_ROOT/fakebin"
mkdir -p "$FAKE_BIN"

cat >"$FAKE_BIN/security" <<'EOF'
#!/bin/sh
set -eu
if [ -n "${FAKE_LOG_DIR:-}" ]; then
    printf '%s\n' "$*" >>"$FAKE_LOG_DIR/security.log"
fi
if [ "${FAKE_SECURITY_EXIT:-0}" -ne 0 ]; then
    printf '%s\n' "${FAKE_SECURITY_ERR:-security command failed}" >&2
    exit "${FAKE_SECURITY_EXIT}"
fi
if [ -n "${FAKE_SECURITY_OUTPUT+set}" ]; then
    printf '%s\n' "$FAKE_SECURITY_OUTPUT"
else
    cat <<'OUT'
  1) AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA "Branchline Env Cert"
  2) BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB "Branchline Local Cert"
  3) CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC "Branchline Main Cert"
     3 valid identities found
OUT
fi
EOF
chmod +x "$FAKE_BIN/security"

cat >"$FAKE_BIN/go" <<'EOF'
#!/bin/sh
set -eu
if [ "${1:-}" = 'env' ] && [ "${2:-}" = 'GOARCH' ]; then
    printf '%s\n' "${FAKE_GOARCH:-arm64}"
    exit 0
fi

if [ -n "${FAKE_LOG_DIR:-}" ]; then
    printf '%s\n' "$*" >>"$FAKE_LOG_DIR/go_actions.log"
fi

if [ "${1:-}" = 'tool' ] && [ "${2:-}" = 'mygo' ] && [ "${3:-}" = 'dev' ]; then
    exit 0
fi

if [ "${1:-}" = 'tool' ] && [ "${2:-}" = 'mygo' ] && [ "${3:-}" = 'build' ]; then
    app_dir="build/darwin-${FAKE_GOARCH:-arm64}/Branchline.app"
    if [ "${FAKE_GO_HANG:-0}" = '1' ]; then
        mkdir -p "$app_dir/Contents/MacOS"
        : >"$app_dir/Contents/MacOS/Branchline"
        python3 -c '
import os, signal, sys, time
signal.signal(signal.SIGTERM, signal.SIG_IGN)
with open(sys.argv[1], "w", encoding="utf-8") as f:
    f.write(str(os.getpid()))
time.sleep(60)
' "$FAKE_HANG_PID_FILE" &
        wait
        exit 0
    fi
    if [ "${FAKE_GO_FAIL:-0}" = '1' ]; then
        mkdir -p "$app_dir/Contents/MacOS"
        : >"$app_dir/Contents/MacOS/Branchline"
        echo 'simulated mygo build failure' >&2
        exit 1
    fi
    mkdir -p "$app_dir/Contents/MacOS" "$app_dir/Contents/Resources"
    printf '#!/bin/sh\nexit 0\n' >"$app_dir/Contents/MacOS/Branchline"
    chmod +x "$app_dir/Contents/MacOS/Branchline"
    printf 'icns-placeholder-data' >"$app_dir/Contents/Resources/AppIcon.icns"
    printf 'car-placeholder-data' >"$app_dir/Contents/Resources/Assets.car"
    cp LICENSE "$app_dir/Contents/Resources/LICENSE"
    cp THIRD_PARTY_NOTICES "$app_dir/Contents/Resources/THIRD_PARTY_NOTICES"
    cat >"$app_dir/Contents/Info.plist" <<'PLIST'
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
    exit 0
fi

echo "Unexpected fake go invocation: $*" >&2
exit 99
EOF
chmod +x "$FAKE_BIN/go"

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
        if [ "${FAKE_CODESIGN_DV_EXIT:-0}" -ne 0 ]; then
            echo 'codesign -dv failed' >&2
            exit "${FAKE_CODESIGN_DV_EXIT}"
        fi
        {
            echo 'Executable=build/darwin-arm64/Branchline.app/Contents/MacOS/Branchline'
            if [ "${FAKE_DV_IDENTIFIER:-com.zhdsmy.branchline}" != '__OMIT__' ]; then
                printf 'Identifier=%s\n' "${FAKE_DV_IDENTIFIER:-com.zhdsmy.branchline}"
            fi
            echo 'Format=app bundle with Mach-O thin (arm64)'
            if [ "${FAKE_DV_FLAGS:-0x10000(runtime)}" != '__OMIT__' ]; then
                printf 'CodeDirectory v=20500 size=1234 flags=%s hashes=42+3 location=embedded\n' "${FAKE_DV_FLAGS:-0x10000(runtime)}"
            fi
            if [ -n "${FAKE_DV_SIGNATURE:-}" ]; then
                printf 'Signature=%s\n' "$FAKE_DV_SIGNATURE"
            else
                echo 'Signature size=1800'
            fi
            case "${FAKE_DV_AUTHORITY:-Branchline Test Development}" in
                __OMIT__) ;;
                __EMPTY__) printf 'Authority=\n' ;;
                *) printf 'Authority=%s\n' "${FAKE_DV_AUTHORITY:-Branchline Test Development}" ;;
            esac
            if [ "${FAKE_DV_TEAM:-not set}" != '__OMIT__' ]; then
                printf 'TeamIdentifier=%s\n' "${FAKE_DV_TEAM:-not set}"
            fi
        } >&2
        exit 0
        ;;
    -dr)
        if [ "${FAKE_CODESIGN_DR_EXIT:-0}" -ne 0 ]; then
            echo 'codesign -dr failed' >&2
            exit "${FAKE_CODESIGN_DR_EXIT}"
        fi
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

make_workspace() {
    ws="$TEST_ROOT/$1"
    mkdir -p "$ws/scripts" "$ws/logs"
    cp "$PKG_SCRIPT_SRC" "$ws/scripts/package-macos.sh"
    cp "$MACOS_SCRIPT_SRC" "$ws/scripts/macos.sh"
    cp "$MYGO_JSON_SRC" "$ws/mygo.json"
    cp "$REPO_ROOT/LICENSE" "$ws/LICENSE"
    cp "$REPO_ROOT/THIRD_PARTY_NOTICES" "$ws/THIRD_PARTY_NOTICES"
    printf '%s' "$ws"
}

make_valid_bundle() {
    ws=$1
    app="$ws/build/darwin-arm64/Branchline.app"
    rm -rf "$app"
    mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
    printf '#!/bin/sh\nexit 0\n' >"$app/Contents/MacOS/Branchline"
    chmod +x "$app/Contents/MacOS/Branchline"
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

assert_contains() {
    haystack=$1
    needle=$2
    case "$haystack" in
        *"$needle"*) return 0 ;;
        *) return 1 ;;
    esac
}

run_expect_fail() {
    name=$1
    expected_substr=$2
    ws=$3
    shift 3
    status=0
    out=$(env -i PATH="$BASE_PATH" HOME="$HOME" TMPDIR="${TMPDIR:-/tmp}" FAKE_LOG_DIR="$ws/logs" "$@" 2>&1) || status=$?
    if [ "$status" -eq 0 ]; then
        fail "$name" "expected non-zero exit, got 0. Output: $out"
        return 1
    fi
    if [ -n "$expected_substr" ] && ! assert_contains "$out" "$expected_substr"; then
        fail "$name" "expected output containing '$expected_substr', got: $out"
        return 1
    fi
    return 0
}

# 1. CLI usage & rejection of dev subcommand on package-macos.sh
ws=$(make_workspace cli)
ok=1
for bad_cmd in '' 'dev' 'unknown'; do
    status=0
    if [ -z "$bad_cmd" ]; then
        out=$(env -i PATH="$BASE_PATH" HOME="$HOME" sh "$ws/scripts/package-macos.sh" 2>&1) || status=$?
    else
        out=$(env -i PATH="$BASE_PATH" HOME="$HOME" sh "$ws/scripts/package-macos.sh" "$bad_cmd" 2>&1) || status=$?
    fi
    if [ "$status" -ne 2 ]; then
        fail "cli_usage_${bad_cmd:-empty}" "expected exit 2, got $status: $out"
        ok=0
    fi
done
[ "$ok" -eq 1 ] && pass 'package-macos.sh validates args and rejects dev subcommand'

# 2. Identity rejection cases must fail without invoking go tool mygo build or creating .app
ws=$(make_workspace reject_missing)
if run_expect_fail 'missing_identity' 'No macOS code-signing identity' "$ws" sh "$ws/scripts/package-macos.sh" build; then
    if [ -f "$ws/logs/go_actions.log" ] || [ -e "$ws/build" ]; then
        fail 'missing_identity' 'go build or build/ was touched despite missing identity'
    else
        pass 'missing identity fails before go build and produces no bundle'
    fi
fi

ws=$(make_workspace reject_empty)
if run_expect_fail 'empty_env_identity' 'set but empty' "$ws" env BRANCHLINE_SIGN_IDENTITY='' sh "$ws/scripts/package-macos.sh" build \
    && run_expect_fail 'whitespace_env_identity' 'set but empty' "$ws" env BRANCHLINE_SIGN_IDENTITY='   ' sh "$ws/scripts/package-macos.sh" build; then
    if [ -f "$ws/logs/go_actions.log" ] || [ -e "$ws/build" ]; then
        fail 'empty_env_identity' 'go build or build/ was touched'
    else
        pass 'empty/whitespace BRANCHLINE_SIGN_IDENTITY fails without building'
    fi
fi

ws=$(make_workspace reject_adhoc)
if run_expect_fail 'adhoc_dash' 'Ad-hoc signing' "$ws" env BRANCHLINE_SIGN_IDENTITY='-' sh "$ws/scripts/package-macos.sh" build; then
    if [ -f "$ws/logs/go_actions.log" ] || [ -e "$ws/build" ]; then
        fail 'adhoc_dash' 'go build or build/ was touched for "-"'
    else
        pass 'ad-hoc "-" identity is explicitly forbidden without building'
    fi
fi

ws=$(make_workspace reject_format)
if run_expect_fail 'non_hex_sha1' 'hexadecimal' "$ws" env BRANCHLINE_SIGN_IDENTITY='GGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGG' sh "$ws/scripts/package-macos.sh" build \
    && run_expect_fail 'short_sha1' 'length (39)' "$ws" env BRANCHLINE_SIGN_IDENTITY='AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' sh "$ws/scripts/package-macos.sh" build \
    && run_expect_fail 'long_sha1' 'length (41)' "$ws" env BRANCHLINE_SIGN_IDENTITY='AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' sh "$ws/scripts/package-macos.sh" build \
    && run_expect_fail 'space_inside_sha1' 'hexadecimal' "$ws" env BRANCHLINE_SIGN_IDENTITY='AAAAAAAAAAAAAAAAAAAA AAAAAAAAAAAAAAAAAAAA' sh "$ws/scripts/package-macos.sh" build; then
    if [ -f "$ws/logs/go_actions.log" ] || [ -e "$ws/build" ]; then
        fail 'invalid_sha1_formats' 'go build or build/ was touched'
    else
        pass 'invalid SHA-1 formats fail without building'
    fi
fi

ws=$(make_workspace reject_not_found)
if run_expect_fail 'not_in_keychain' 'not a valid code-signing identity' "$ws" env BRANCHLINE_SIGN_IDENTITY="$SHA_MISSING" sh "$ws/scripts/package-macos.sh" build; then
    if [ -f "$ws/logs/go_actions.log" ] || [ -e "$ws/build" ]; then
        fail 'not_in_keychain' 'go build or build/ was touched for unknown identity'
    else
        pass 'unknown SHA-1 not in keychain fails without building'
    fi
fi

ws=$(make_workspace reject_empty_file)
mkdir -p "$ws/.mygo"
printf '  \n\t\n' >"$ws/.mygo/macos-signing-identity"
if run_expect_fail 'empty_identity_file' 'identity file is empty' "$ws" sh "$ws/scripts/package-macos.sh" build; then
    if [ -f "$ws/logs/go_actions.log" ]; then
        fail 'empty_identity_file' 'go build was invoked'
    else
        pass 'empty .mygo/macos-signing-identity fails without building'
    fi
fi

# 3. Local file whitespace/lowercase normalization and env precedence
ws=$(make_workspace precedence)
mkdir -p "$ws/.mygo"
printf '  %s  \r\n' "$SHA_LOCAL_LOWER" >"$ws/.mygo/macos-signing-identity"
out=$(env -i PATH="$BASE_PATH" HOME="$HOME" TMPDIR="${TMPDIR:-/tmp}" FAKE_LOG_DIR="$ws/logs" sh "$ws/scripts/package-macos.sh" check 2>&1)
if assert_contains "$out" "Code-signing identity available: $SHA_LOCAL \"Branchline Local Cert\""; then
    pass 'local .mygo/macos-signing-identity strips whitespace and normalizes case'
else
    fail 'local_identity_normalize' "unexpected output: $out"
fi

out=$(env -i PATH="$BASE_PATH" HOME="$HOME" TMPDIR="${TMPDIR:-/tmp}" FAKE_LOG_DIR="$ws/logs" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV_LOWER" \
    FAKE_EXPECT_VERIFY_REQ="=certificate leaf = H\"$SHA_ENV\"" \
    FAKE_DR_LINE="designated => identifier \"com.zhdsmy.branchline\" and certificate leaf = H\"$SHA_ENV_LOWER\"" \
    sh "$ws/scripts/package-macos.sh" build 2>&1)
go_log=$(cat "$ws/logs/go_actions.log")
if assert_contains "$go_log" "tool mygo build -platform darwin/arm64 -sign $SHA_ENV -skip-dmg" \
    && assert_contains "$out" 'Authority: Branchline Test Development' \
    && assert_contains "$out" 'TeamIdentifier: not set' \
    && assert_contains "$out" 'CFBundleShortVersionString / CFBundleVersion: 0.2.0 / 0.2.0'; then
    pass 'BRANCHLINE_SIGN_IDENTITY overrides local file and build+verify passes'
else
    fail 'env_precedence_and_build' "go_log=$go_log out=$out"
fi

# 4. Real git worktree fallback to main repo .mygo/macos-signing-identity
wt_main="$TEST_ROOT/git-main"
wt_linked="$TEST_ROOT/git-worktree"
mkdir -p "$wt_main/scripts" "$wt_main/logs" "$wt_linked"
cp "$PKG_SCRIPT_SRC" "$wt_main/scripts/package-macos.sh"
cp "$MACOS_SCRIPT_SRC" "$wt_main/scripts/macos.sh"
cp "$MYGO_JSON_SRC" "$wt_main/mygo.json"
git -C "$wt_main" init -q
git -C "$wt_main" config user.email 'test@example.com'
git -C "$wt_main" config user.name 'Branchline Test'
git -C "$wt_main" add scripts/package-macos.sh scripts/macos.sh mygo.json
git -C "$wt_main" commit -q -m 'init'
git -C "$wt_main" worktree add -q -b test-wt "$wt_linked"
mkdir -p "$wt_main/.mygo" "$wt_linked/logs"
printf '%s\n' "$SHA_MAIN" >"$wt_main/.mygo/macos-signing-identity"

out=$(env -i PATH="$BASE_PATH" HOME="$HOME" TMPDIR="${TMPDIR:-/tmp}" FAKE_LOG_DIR="$wt_linked/logs" sh "$wt_linked/scripts/package-macos.sh" check 2>&1)
if assert_contains "$out" "Code-signing identity available: $SHA_MAIN \"Branchline Main Cert\""; then
    pass 'linked git worktree falls back to main repo .mygo/macos-signing-identity'
else
    fail 'worktree_fallback' "unexpected output: $out"
fi

if run_expect_fail 'worktree_explicit_empty_env' 'set but empty' "$wt_linked" env BRANCHLINE_SIGN_IDENTITY='' sh "$wt_linked/scripts/package-macos.sh" check; then
    pass 'explicitly empty BRANCHLINE_SIGN_IDENTITY fails closed even in worktree with main config'
fi

mkdir -p "$wt_linked/.mygo"
printf '%s\n' "$SHA_LOCAL" >"$wt_linked/.mygo/macos-signing-identity"
out=$(env -i PATH="$BASE_PATH" HOME="$HOME" TMPDIR="${TMPDIR:-/tmp}" FAKE_LOG_DIR="$wt_linked/logs" sh "$wt_linked/scripts/package-macos.sh" check 2>&1)
if assert_contains "$out" "Code-signing identity available: $SHA_LOCAL \"Branchline Local Cert\""; then
    pass 'worktree local .mygo/macos-signing-identity overrides main repo file'
else
    fail 'worktree_local_override' "unexpected output: $out"
fi

wt_fail_closed_ok=1
printf '  \n\t\n' >"$wt_linked/.mygo/macos-signing-identity"
run_expect_fail 'worktree_empty_local_file' 'identity file is empty' "$wt_linked" sh "$wt_linked/scripts/package-macos.sh" build || wt_fail_closed_ok=0
printf 'not-a-valid-sha1\n' >"$wt_linked/.mygo/macos-signing-identity"
run_expect_fail 'worktree_invalid_local_file' 'hexadecimal' "$wt_linked" sh "$wt_linked/scripts/package-macos.sh" build || wt_fail_closed_ok=0
printf '%s\n' '-' >"$wt_linked/.mygo/macos-signing-identity"
run_expect_fail 'worktree_adhoc_local_file' 'Ad-hoc signing' "$wt_linked" sh "$wt_linked/scripts/package-macos.sh" build || wt_fail_closed_ok=0
if [ "$wt_fail_closed_ok" -eq 1 ]; then
    if [ -f "$wt_linked/logs/go_actions.log" ] || [ -e "$wt_linked/build" ]; then
        fail 'worktree_local_fail_closed' 'fell back to main repo or invoked go build when worktree identity file was empty/invalid'
    else
        pass 'empty or invalid worktree .mygo/macos-signing-identity fails closed without falling back to main repo'
    fi
fi

# 5. scripts/macos.sh delegation for check/build/verify and dev identity check
ws=$(make_workspace macos_delegation)
out_check=$(env -i PATH="$BASE_PATH" HOME="$HOME" TMPDIR="${TMPDIR:-/tmp}" FAKE_LOG_DIR="$ws/logs" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/macos.sh" check 2>&1)
out_build=$(env -i PATH="$BASE_PATH" HOME="$HOME" TMPDIR="${TMPDIR:-/tmp}" FAKE_LOG_DIR="$ws/logs" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/macos.sh" build 2>&1)
out_verify=$(env -i PATH="$BASE_PATH" HOME="$HOME" TMPDIR="${TMPDIR:-/tmp}" FAKE_LOG_DIR="$ws/logs" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" FAKE_DV_TEAM='ABCDE12345' sh "$ws/scripts/macos.sh" verify 2>&1)
out_dev=$(env -i PATH="$BASE_PATH" HOME="$HOME" TMPDIR="${TMPDIR:-/tmp}" FAKE_LOG_DIR="$ws/logs" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/macos.sh" dev 2>&1)
go_log=$(cat "$ws/logs/go_actions.log")
if assert_contains "$out_check" "$SHA_ENV" \
    && assert_contains "$out_build" 'Code signature verified:' \
    && assert_contains "$out_verify" 'TeamIdentifier: ABCDE12345 (Apple Team ID)' \
    && assert_contains "$go_log" "tool mygo dev -sign $SHA_ENV"; then
    pass 'scripts/macos.sh delegates check/build/verify and preserves dev launch with validated identity'
else
    fail 'macos_sh_delegation' "check=$out_check build=$out_build verify=$out_verify dev=$out_dev go_log=$go_log"
fi

# 6. verify negative cases: codesign assertions
ws=$(make_workspace verify_codesign)
if run_expect_fail 'verify_missing_app' 'App bundle not found' "$ws" env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/package-macos.sh" verify; then
    pass 'verify fails when target .app bundle is missing'
fi

make_valid_bundle "$ws"

if run_expect_fail 'verify_codesign_verify_fail' 'Strict codesign verification failed' "$ws" \
        env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" FAKE_CODESIGN_VERIFY_FAIL=1 sh "$ws/scripts/package-macos.sh" verify \
    && run_expect_fail 'verify_adhoc_signature' 'Signature=adhoc' "$ws" \
        env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" FAKE_DV_SIGNATURE=adhoc sh "$ws/scripts/package-macos.sh" verify \
    && run_expect_fail 'verify_adhoc_flag' 'Ad-hoc flag present' "$ws" \
        env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" FAKE_DV_FLAGS='0x2(adhoc)' sh "$ws/scripts/package-macos.sh" verify \
    && run_expect_fail 'verify_missing_runtime' "Hardened Runtime flag 'runtime' missing" "$ws" \
        env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" FAKE_DV_FLAGS='0x0(none)' sh "$ws/scripts/package-macos.sh" verify \
    && run_expect_fail 'verify_missing_authority' 'Missing non-empty Authority=' "$ws" \
        env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" FAKE_DV_AUTHORITY='__OMIT__' sh "$ws/scripts/package-macos.sh" verify \
    && run_expect_fail 'verify_empty_authority' 'Empty Authority=' "$ws" \
        env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" FAKE_DV_AUTHORITY='__EMPTY__' sh "$ws/scripts/package-macos.sh" verify \
    && run_expect_fail 'verify_wrong_identifier' 'Signature Identifier mismatch' "$ws" \
        env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" FAKE_DV_IDENTIFIER='com.example.other' sh "$ws/scripts/package-macos.sh" verify \
    && run_expect_fail 'verify_missing_team' 'Missing TeamIdentifier=' "$ws" \
        env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" FAKE_DV_TEAM='__OMIT__' sh "$ws/scripts/package-macos.sh" verify \
    && run_expect_fail 'verify_dr_wrong_id' 'does not lock identifier' "$ws" \
        env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" FAKE_DR_LINE="designated => identifier \"com.example.other\" and certificate leaf = H\"$SHA_ENV_LOWER\"" sh "$ws/scripts/package-macos.sh" verify \
    && run_expect_fail 'verify_dr_wrong_hash' 'does not lock certificate leaf' "$ws" \
        env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" FAKE_DR_LINE="designated => identifier \"com.zhdsmy.branchline\" and certificate leaf = H\"$SHA_LOCAL_LOWER\"" sh "$ws/scripts/package-macos.sh" verify \
    && run_expect_fail 'verify_dr_uppercase_hash' 'does not lock certificate leaf' "$ws" \
        env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" FAKE_DR_LINE="designated => identifier \"com.zhdsmy.branchline\" and certificate leaf = H\"$SHA_ENV\"" sh "$ws/scripts/package-macos.sh" verify \
    && run_expect_fail 'verify_dr_cdhash' 'unexpectedly pins cdhash' "$ws" \
        env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" FAKE_DR_LINE="designated => cdhash H\"$SHA_ENV_LOWER\"" sh "$ws/scripts/package-macos.sh" verify; then
    pass 'verify rejects ad-hoc, missing runtime/Authority/TeamIdentifier, wrong Identifier, and mismatched designated requirement'
fi

# 7. verify negative cases: Info.plist and bundle structure
ws=$(make_workspace verify_plist)
plist_ok=1

make_valid_bundle "$ws"
printf '<plist><broken' >"$ws/build/darwin-arm64/Branchline.app/Contents/Info.plist"
run_expect_fail 'plist_malformed' 'plutil -lint' "$ws" env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/package-macos.sh" verify || plist_ok=0

mutate_plist_and_check() {
    case_name=$1
    expect_msg=$2
    py_mut=$3
    make_valid_bundle "$ws"
    python3 - "$ws/build/darwin-arm64/Branchline.app/Contents/Info.plist" "$py_mut" <<'PY'
import plistlib, sys
p, expr = sys.argv[1], sys.argv[2]
with open(p, "rb") as f:
    d = plistlib.load(f)
exec(expr, {"d": d})
with open(p, "wb") as f:
    plistlib.dump(d, f)
PY
    run_expect_fail "$case_name" "$expect_msg" "$ws" env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/package-macos.sh" verify || plist_ok=0
}

mutate_plist_and_check 'plist_bad_short_version' 'CFBundleShortVersionString' 'd["CFBundleShortVersionString"] = "0.1.0"'
mutate_plist_and_check 'plist_empty_version' 'CFBundleVersion' 'd["CFBundleVersion"] = ""'
mutate_plist_and_check 'plist_bad_bundle_id' 'CFBundleIdentifier' 'd["CFBundleIdentifier"] = "com.example.wrong"'
mutate_plist_and_check 'plist_bad_name' 'CFBundleName' 'd["CFBundleName"] = "OtherApp"'
mutate_plist_and_check 'plist_bad_display_name' 'CFBundleDisplayName' 'd["CFBundleDisplayName"] = "OtherApp"'
mutate_plist_and_check 'plist_bad_executable' 'CFBundleExecutable' 'd["CFBundleExecutable"] = "OtherBin"'
mutate_plist_and_check 'plist_bad_icon' 'CFBundleIconFile' 'd["CFBundleIconFile"] = "other.icns"'
mutate_plist_and_check 'plist_bad_icon_name' 'CFBundleIconName' 'd["CFBundleIconName"] = "OtherIcon"'
mutate_plist_and_check 'plist_missing_icon_name' 'CFBundleIconName' 'del d["CFBundleIconName"]'
mutate_plist_and_check 'plist_missing_url_scheme' 'CFBundleURLTypes is missing expected URL scheme' 'd["CFBundleURLTypes"] = [{"CFBundleURLName": "com.zhdsmy.branchline", "CFBundleURLSchemes": ["other"]}]'
mutate_plist_and_check 'plist_empty_min_sys' 'LSMinimumSystemVersion' 'd["LSMinimumSystemVersion"] = ""'

make_valid_bundle "$ws"
chmod -x "$ws/build/darwin-arm64/Branchline.app/Contents/MacOS/Branchline"
run_expect_fail 'bundle_non_exec' 'not executable' "$ws" env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/package-macos.sh" verify || plist_ok=0

make_valid_bundle "$ws"
rm -f "$ws/build/darwin-arm64/Branchline.app/Contents/MacOS/Branchline"
run_expect_fail 'bundle_missing_exec' 'Missing bundle executable' "$ws" env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/package-macos.sh" verify || plist_ok=0

make_valid_bundle "$ws"
: >"$ws/build/darwin-arm64/Branchline.app/Contents/Resources/AppIcon.icns"
run_expect_fail 'bundle_empty_icon' 'Bundle icon is empty' "$ws" env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/package-macos.sh" verify || plist_ok=0

make_valid_bundle "$ws"
rm -f "$ws/build/darwin-arm64/Branchline.app/Contents/Resources/AppIcon.icns"
run_expect_fail 'bundle_missing_icon' 'Missing bundle icon' "$ws" env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/package-macos.sh" verify || plist_ok=0

make_valid_bundle "$ws"
rm -f "$ws/build/darwin-arm64/Branchline.app/Contents/Resources/Assets.car"
run_expect_fail 'bundle_missing_assets_car' 'Missing Assets.car' "$ws" env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/package-macos.sh" verify || plist_ok=0

make_valid_bundle "$ws"
: >"$ws/build/darwin-arm64/Branchline.app/Contents/Resources/Assets.car"
run_expect_fail 'bundle_empty_assets_car' 'Assets.car is empty' "$ws" env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/package-macos.sh" verify || plist_ok=0

make_valid_bundle "$ws"
rm -f "$ws/build/darwin-arm64/Branchline.app/Contents/Resources/THIRD_PARTY_NOTICES"
run_expect_fail 'bundle_missing_notices' 'Missing bundle license file' "$ws" env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/package-macos.sh" verify || plist_ok=0

make_valid_bundle "$ws"
: >"$ws/build/darwin-arm64/Branchline.app/Contents/Resources/THIRD_PARTY_NOTICES"
run_expect_fail 'bundle_empty_notices' 'Bundle license file is empty' "$ws" env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/package-macos.sh" verify || plist_ok=0

make_valid_bundle "$ws"
printf 'stale third-party notices\n' >"$ws/build/darwin-arm64/Branchline.app/Contents/Resources/THIRD_PARTY_NOTICES"
run_expect_fail 'bundle_mismatched_notices' 'differs from repository copy' "$ws" env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/package-macos.sh" verify || plist_ok=0

make_valid_bundle "$ws"
rm -f "$ws/build/darwin-arm64/Branchline.app/Contents/Resources/LICENSE"
run_expect_fail 'bundle_missing_license' 'Missing bundle license file' "$ws" env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" sh "$ws/scripts/package-macos.sh" verify || plist_ok=0

make_valid_bundle "$ws"

[ "$plist_ok" -eq 1 ] && pass 'verify rejects malformed Info.plist, field/version/URL mismatches, broken bundle files, and missing/empty/stale license notices'

# 8. Build removes unverified bundle when build or post-build verification fails
ws=$(make_workspace cleanup_on_fail)
if run_expect_fail 'build_fail_cleans_bundle' 'Removed unverified bundle after build failure' "$ws" \
        env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" FAKE_GO_FAIL=1 sh "$ws/scripts/package-macos.sh" build \
    && [ ! -e "$ws/build/darwin-arm64/Branchline.app" ]; then
    pass 'build removes unverified bundle after mygo build failure'
else
    fail 'build_fail_cleans_bundle' 'bundle still exists or wrong error'
fi

if run_expect_fail 'verify_fail_cleans_bundle' 'Removed unverified bundle after verification failure' "$ws" \
        env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" FAKE_DV_SIGNATURE=adhoc sh "$ws/scripts/package-macos.sh" build \
    && [ ! -e "$ws/build/darwin-arm64/Branchline.app" ] \
    && run_expect_fail 'verify_strict_fail_cleans_bundle' 'Removed unverified bundle after verification failure' "$ws" \
        env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" FAKE_CODESIGN_VERIFY_FAIL=1 sh "$ws/scripts/package-macos.sh" build \
    && [ ! -e "$ws/build/darwin-arm64/Branchline.app" ]; then
    pass 'build removes unverified bundle if post-build verification fails (ad-hoc or codesign --verify)'
else
    fail 'verify_fail_cleans_bundle' 'bundle still exists or wrong error'
fi

# 9. Non-interactive build timeout guard: rejects bad values and terminates hung process group
ws=$(make_workspace timeout_guard)
if run_expect_fail 'timeout_invalid_zero' 'Invalid BRANCHLINE_BUILD_TIMEOUT' "$ws" \
        env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" BRANCHLINE_BUILD_TIMEOUT=0 sh "$ws/scripts/package-macos.sh" build \
    && run_expect_fail 'timeout_invalid_text' 'Invalid BRANCHLINE_BUILD_TIMEOUT' "$ws" \
        env BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" BRANCHLINE_BUILD_TIMEOUT=abc sh "$ws/scripts/package-macos.sh" build; then
    if [ -f "$ws/logs/go_actions.log" ]; then
        fail 'timeout_invalid' 'go build was invoked with invalid timeout'
    else
        pass 'invalid BRANCHLINE_BUILD_TIMEOUT values fail fast before invoking go'
    fi
fi

hang_pid_file="$ws/hang_grandchild.pid"
start_ts=$(python3 -c 'import time; print(int(time.monotonic()))')
status=0
out=$(env -i PATH="$BASE_PATH" HOME="$HOME" TMPDIR="${TMPDIR:-/tmp}" FAKE_LOG_DIR="$ws/logs" \
    BRANCHLINE_SIGN_IDENTITY="$SHA_ENV" \
    BRANCHLINE_BUILD_TIMEOUT=1 \
    FAKE_GO_HANG=1 \
    FAKE_HANG_PID_FILE="$hang_pid_file" \
    sh "$ws/scripts/package-macos.sh" build 2>&1) || status=$?
end_ts=$(python3 -c 'import time; print(int(time.monotonic()))')
elapsed=$((end_ts - start_ts))

timeout_ok=1
if [ "$status" -ne 124 ]; then
    fail 'timeout_exit_code' "expected exit 124, got $status: $out"
    timeout_ok=0
fi
if ! assert_contains "$out" 'timed out after 1s' || ! assert_contains "$out" 'keychain'; then
    fail 'timeout_message' "missing timeout/keychain guidance: $out"
    timeout_ok=0
fi
if [ "$elapsed" -gt 15 ]; then
    fail 'timeout_duration' "expected bounded completion well under 15s, took ${elapsed}s"
    timeout_ok=0
fi
if [ ! -f "$hang_pid_file" ]; then
    fail 'timeout_pid_file' 'hanging grandchild did not start'
    timeout_ok=0
else
    grandchild_pid=$(cat "$hang_pid_file")
    if kill -0 "$grandchild_pid" 2>/dev/null; then
        kill -9 "$grandchild_pid" 2>/dev/null || true
        fail 'timeout_process_leak' "grandchild pid $grandchild_pid survived timeout termination"
        timeout_ok=0
    fi
fi
if [ -e "$ws/build/darwin-arm64/Branchline.app" ]; then
    fail 'timeout_bundle_cleanup' 'partial .app remained after build timeout'
    timeout_ok=0
fi
[ "$timeout_ok" -eq 1 ] && pass "build timeout (1s) terminates stubborn process group (${elapsed}s), cleans partial bundle, and prints keychain guidance"

printf '\nSummary: %d passed, %d failed\n' "$PASS_COUNT" "$FAIL_COUNT"
[ "$FAIL_COUNT" -eq 0 ]
