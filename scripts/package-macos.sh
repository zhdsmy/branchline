#!/bin/sh
# Non-interactive macOS signing build and verification entry point for Branchline.
# Never falls back to ad-hoc signing ("-"), never modifies keychains/trust, and
# never launches any GUI application.
set -eu

usage() {
    echo 'Usage: [BRANCHLINE_SIGN_IDENTITY=<40-char SHA-1>] [BRANCHLINE_BUILD_TIMEOUT=<seconds>] sh scripts/package-macos.sh check|build|verify' >&2
    exit 2
}

print_identity_help() {
    cat >&2 <<'EOF'
No app was built; ad-hoc signing ("-") is disabled, and no keychain, certificate, or trust settings were modified.
Inspect valid local code-signing identities (read-only):
  security find-identity -v -p codesigning
Then configure a 40-character certificate SHA-1 using either:
  export BRANCHLINE_SIGN_IDENTITY='<40-char SHA-1>'
or by writing it to the git-ignored file:
  .mygo/macos-signing-identity
See docs/macos-development-signing.md for details.
EOF
}

[ "$#" -eq 1 ] || usage
case "$1" in
    check|build|verify) MODE=$1 ;;
    *) usage ;;
esac

[ "$(uname -s)" = Darwin ] || {
    echo 'This entry point requires macOS.' >&2
    exit 1
}

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd -P)
cd "$REPO_ROOT"

load_mygo_config() {
    [ -f "$REPO_ROOT/mygo.json" ] || {
        echo "Missing mygo.json in $REPO_ROOT" >&2
        exit 1
    }
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
identifier = req_str("identifier")
version = req_str("version")
out_dir = cfg.get("out") or "build"
if not isinstance(out_dir, str) or not out_dir.strip() or "\n" in out_dir or "\r" in out_dir:
    sys.stderr.write(f"Invalid {path}: 'out' must be a single-line string\n")
    sys.exit(1)

print(name)
print(identifier)
print(version)
print(out_dir)
PY
    )

    APP_NAME=$(printf '%s\n' "$cfg_lines" | sed -n '1p')
    APP_IDENTIFIER=$(printf '%s\n' "$cfg_lines" | sed -n '2p')
    APP_VERSION=$(printf '%s\n' "$cfg_lines" | sed -n '3p')
    APP_OUT=$(printf '%s\n' "$cfg_lines" | sed -n '4p')

    ARCH=$(go env GOARCH)
    case "$ARCH" in
        arm64|amd64) ;;
        *)
            echo "Unsupported macOS architecture from 'go env GOARCH': $ARCH" >&2
            exit 1 ;;
    esac
    APP_PATH="$APP_OUT/darwin-$ARCH/$APP_NAME.app"
}

resolve_identity() {
    IDENTITY_SOURCE=''
    raw_identity=''

    if [ "${BRANCHLINE_SIGN_IDENTITY+set}" = set ]; then
        IDENTITY_SOURCE='BRANCHLINE_SIGN_IDENTITY'
        raw_identity=$BRANCHLINE_SIGN_IDENTITY
    elif [ -f "$REPO_ROOT/.mygo/macos-signing-identity" ]; then
        IDENTITY_SOURCE="$REPO_ROOT/.mygo/macos-signing-identity"
        raw_identity=$(cat "$REPO_ROOT/.mygo/macos-signing-identity")
    else
        git_top=$(git -C "$REPO_ROOT" rev-parse --show-toplevel 2>/dev/null || true)
        if [ -n "$git_top" ]; then
            git_top=$(CDPATH= cd -- "$git_top" 2>/dev/null && pwd -P || true)
        fi
        if [ "$git_top" = "$REPO_ROOT" ]; then
            common_dir=$(git -C "$REPO_ROOT" rev-parse --git-common-dir 2>/dev/null || true)
            if [ -n "$common_dir" ]; then
                case "$common_dir" in
                    /*) ;;
                    *) common_dir="$REPO_ROOT/$common_dir" ;;
                esac
                common_dir_abs=$(CDPATH= cd -- "$common_dir" 2>/dev/null && pwd -P || true)
                if [ -n "$common_dir_abs" ] && [ "$(basename -- "$common_dir_abs")" = '.git' ]; then
                    main_root=$(CDPATH= cd -- "$common_dir_abs/.." 2>/dev/null && pwd -P || true)
                    if [ -n "$main_root" ] && [ "$main_root" != "$REPO_ROOT" ] && [ -f "$main_root/.mygo/macos-signing-identity" ]; then
                        IDENTITY_SOURCE="$main_root/.mygo/macos-signing-identity"
                        raw_identity=$(cat "$main_root/.mygo/macos-signing-identity")
                    fi
                fi
            fi
        fi
    fi

    if [ -z "$IDENTITY_SOURCE" ]; then
        echo 'No macOS code-signing identity is configured.' >&2
        print_identity_help
        exit 1
    fi

    trimmed_identity=$(printf '%s' "$raw_identity" | python3 -c 'import sys; sys.stdout.write(sys.stdin.read().strip())')

    if [ -z "$trimmed_identity" ]; then
        if [ "$IDENTITY_SOURCE" = 'BRANCHLINE_SIGN_IDENTITY' ]; then
            echo 'BRANCHLINE_SIGN_IDENTITY is set but empty (unset it to use .mygo/macos-signing-identity).' >&2
        else
            echo "Signing identity file is empty: $IDENTITY_SOURCE" >&2
        fi
        print_identity_help
        exit 1
    fi

    if [ "$trimmed_identity" = '-' ]; then
        echo "Ad-hoc signing (\"-\") from $IDENTITY_SOURCE is forbidden." >&2
        print_identity_help
        exit 1
    fi

    case "$trimmed_identity" in
        *[!0-9a-fA-F]*)
            echo "Invalid signing identity format from $IDENTITY_SOURCE: must contain only 40 hexadecimal SHA-1 characters." >&2
            print_identity_help
            exit 1 ;;
    esac

    if [ "${#trimmed_identity}" -ne 40 ]; then
        echo "Invalid signing identity length (${#trimmed_identity}) from $IDENTITY_SOURCE: expected 40 hexadecimal SHA-1 characters." >&2
        print_identity_help
        exit 1
    fi

    IDENTITY=$(printf '%s' "$trimmed_identity" | tr '[:lower:]' '[:upper:]')
    IDENTITY_LOWER=$(printf '%s' "$IDENTITY" | tr '[:upper:]' '[:lower:]')

    sec_status=0
    identities=$(security find-identity -v -p codesigning 2>&1) || sec_status=$?
    if [ "$sec_status" -ne 0 ]; then
        echo "Failed to query code-signing identities via 'security find-identity -v -p codesigning' (exit $sec_status):" >&2
        printf '%s\n' "$identities" >&2
        print_identity_help
        exit 1
    fi

    identity_line=$(printf '%s\n' "$identities" | awk -v expected="$IDENTITY" '
        $1 ~ /^[0-9]+\)$/ && toupper($2) == expected && $0 !~ /\(CSSMERR_/ {
            print
            found = 1
            exit
        }
        END { exit !found }
    ') || {
        echo "Signing identity $IDENTITY (from $IDENTITY_SOURCE) is not a valid code-signing identity in the current keychain." >&2
        print_identity_help
        exit 1
    }

    IDENTITY_NAME=$(printf '%s' "$identity_line" | sed -n 's/^.*"\(.*\)".*$/\1/p')
}

cmd_check() {
    if [ -n "$IDENTITY_NAME" ]; then
        printf 'Code-signing identity available: %s "%s"\n' "$IDENTITY" "$IDENTITY_NAME"
    else
        printf 'Code-signing identity available: %s\n' "$IDENTITY"
    fi
    printf 'Identity source: %s\n' "$IDENTITY_SOURCE"
    printf 'Target bundle: %s\n' "$APP_PATH"
}

verify_codesign() {
    verify_cmd_status=0
    verify_cmd_out=$(codesign --verify --deep --strict -R "=certificate leaf = H\"$IDENTITY\"" "$APP_PATH" 2>&1) || verify_cmd_status=$?
    if [ "$verify_cmd_status" -ne 0 ]; then
        echo "Strict codesign verification failed for $APP_PATH (exit $verify_cmd_status):" >&2
        [ -n "$verify_cmd_out" ] && printf '%s\n' "$verify_cmd_out" >&2
        return 1
    fi

    dv_status=0
    dv_out=$(codesign -dv --verbose=4 "$APP_PATH" 2>&1) || dv_status=$?
    if [ "$dv_status" -ne 0 ]; then
        echo "Failed to inspect code signature via 'codesign -dv --verbose=4' (exit $dv_status):" >&2
        [ -n "$dv_out" ] && printf '%s\n' "$dv_out" >&2
        return 1
    fi

    dr_status=0
    dr_out=$(codesign -dr - "$APP_PATH" 2>&1) || dr_status=$?
    if [ "$dr_status" -ne 0 ]; then
        echo "Failed to inspect designated requirement via 'codesign -dr -' (exit $dr_status):" >&2
        [ -n "$dr_out" ] && printf '%s\n' "$dr_out" >&2
        return 1
    fi

    DV_OUTPUT=$dv_out DR_OUTPUT=$dr_out python3 - "$APP_PATH" "$APP_IDENTIFIER" "$IDENTITY_LOWER" <<'PY' || return 1
import os, re, sys

app_path, expected_id, expected_sha1_lower = sys.argv[1:4]
dv_text = os.environ.get("DV_OUTPUT", "")
dr_text = os.environ.get("DR_OUTPUT", "")

errors = []
identifier = None
authorities = []
team_identifier = None
code_directory_line = None
flags_token = None

for raw_line in dv_text.splitlines():
    line = raw_line.rstrip("\r")
    if line.startswith("Signature="):
        sig_val = line[len("Signature="):].strip()
        if sig_val.lower() == "adhoc":
            errors.append("Ad-hoc signature detected (Signature=adhoc); a persistent certificate is required.")
    elif line.startswith("Identifier="):
        identifier = line[len("Identifier="):].strip()
    elif line.startswith("Authority="):
        auth = line[len("Authority="):].strip()
        if not auth:
            errors.append("Empty Authority= entry in codesign -dv output.")
        else:
            authorities.append(auth)
    elif line.startswith("TeamIdentifier="):
        team_identifier = line[len("TeamIdentifier="):].strip()
    elif line.startswith("CodeDirectory "):
        code_directory_line = line

if identifier is None:
    errors.append("Missing Identifier= in codesign -dv output.")
elif identifier != expected_id:
    errors.append(f"Signature Identifier mismatch: got {identifier!r}, expected {expected_id!r}.")

if not authorities:
    errors.append("Missing non-empty Authority= in codesign -dv output.")

if team_identifier is None or not team_identifier:
    errors.append("Missing TeamIdentifier= in codesign -dv output.")

if code_directory_line is None:
    errors.append("Missing CodeDirectory line in codesign -dv output.")
else:
    m = re.search(r"\bflags=(0x[0-9a-fA-F]+\(([^)]*)\))", code_directory_line)
    if not m:
        errors.append(f"Unable to parse flags= in CodeDirectory line: {code_directory_line!r}.")
    else:
        flags_token = m.group(1)
        flag_names = [f.strip().lower() for f in m.group(2).split(",") if f.strip()]
        if "adhoc" in flag_names:
            errors.append(f"Ad-hoc flag present in CodeDirectory ({flags_token}); a persistent certificate is required.")
        if "runtime" not in flag_names:
            errors.append(f"Hardened Runtime flag 'runtime' missing from CodeDirectory ({flags_token}).")

designated_req = None
for raw_line in dr_text.splitlines():
    line = raw_line.strip()
    if line.startswith("designated => "):
        designated_req = line[len("designated => "):].strip()
        break

if designated_req is None:
    errors.append("Missing 'designated =>' line in codesign -dr - output.")
else:
    if re.search(r"\bcdhash\b", designated_req, re.IGNORECASE):
        errors.append(f"Designated requirement unexpectedly pins cdhash instead of certificate identity: {designated_req}")
    expected_id_clause = f'identifier "{expected_id}"'
    if expected_id_clause not in designated_req:
        errors.append(f"Designated requirement does not lock {expected_id_clause}: {designated_req}")
    expected_leaf_clause = f'certificate leaf = H"{expected_sha1_lower}"'
    if expected_leaf_clause not in designated_req:
        errors.append(f"Designated requirement does not lock {expected_leaf_clause}: {designated_req}")

if errors:
    sys.stderr.write(f"Code signature verification failed for {app_path}:\n")
    for err in errors:
        sys.stderr.write(f"  - {err}\n")
    sys.exit(1)

if team_identifier == "not set":
    team_note = "local self-signed development certificate; not a public Apple Developer ID"
elif re.fullmatch(r"[A-Z0-9]{10}", team_identifier):
    team_note = "Apple Team ID"
else:
    team_note = "custom TeamIdentifier"

print(f"Code signature verified: {app_path}")
print(f"  Identifier: {identifier}")
for auth in authorities:
    print(f"  Authority: {auth}")
print(f"  TeamIdentifier: {team_identifier} ({team_note})")
print(f"  CodeDirectory flags: {flags_token}")
print(f"  Designated requirement: {designated_req}")
PY
}

verify_bundle_and_plist() {
    plist_path="$APP_PATH/Contents/Info.plist"
    exec_path="$APP_PATH/Contents/MacOS/$APP_NAME"
    icon_path="$APP_PATH/Contents/Resources/AppIcon.icns"

    [ -f "$plist_path" ] || {
        echo "Missing Info.plist: $plist_path" >&2
        return 1
    }

    lint_status=0
    lint_out=$(plutil -lint "$plist_path" 2>&1) || lint_status=$?
    if [ "$lint_status" -ne 0 ]; then
        echo "Info.plist failed 'plutil -lint' (exit $lint_status):" >&2
        [ -n "$lint_out" ] && printf '%s\n' "$lint_out" >&2
        return 1
    fi

    plist_json_status=0
    plist_json=$(plutil -convert json -o - "$plist_path" 2>&1) || plist_json_status=$?
    if [ "$plist_json_status" -ne 0 ]; then
        echo "Failed to convert Info.plist to JSON via plutil (exit $plist_json_status):" >&2
        [ -n "$plist_json" ] && printf '%s\n' "$plist_json" >&2
        return 1
    fi

    [ -f "$exec_path" ] || {
        echo "Missing bundle executable: $exec_path" >&2
        return 1
    }
    [ -x "$exec_path" ] || {
        echo "Bundle executable is not executable: $exec_path" >&2
        return 1
    }
    [ -f "$icon_path" ] || {
        echo "Missing bundle icon: $icon_path" >&2
        return 1
    }
    [ -s "$icon_path" ] || {
        echo "Bundle icon is empty: $icon_path" >&2
        return 1
    }

    macos_icon=$(python3 - "$REPO_ROOT/mygo.json" <<'PY'
import json, sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    cfg = json.load(f)
macos = cfg.get("macos")
if isinstance(macos, dict) and isinstance(macos.get("icon"), str):
    print(macos["icon"])
PY
    )
    case "$(printf '%s' "$macos_icon" | tr '[:upper:]' '[:lower:]')" in
        *.icon|*.car)
            assets_car="$APP_PATH/Contents/Resources/Assets.car"
            [ -f "$assets_car" ] || {
                echo "Missing Assets.car: $assets_car" >&2
                return 1
            }
            [ -s "$assets_car" ] || {
                echo "Assets.car is empty: $assets_car" >&2
                return 1
            }
            ;;
    esac

    for license_file in LICENSE THIRD_PARTY_NOTICES; do
        repo_file="$REPO_ROOT/$license_file"
        bundle_file="$APP_PATH/Contents/Resources/$license_file"
        [ -f "$repo_file" ] || {
            echo "Missing repository license file: $repo_file" >&2
            return 1
        }
        [ -s "$repo_file" ] || {
            echo "Repository license file is empty: $repo_file" >&2
            return 1
        }
        [ -f "$bundle_file" ] || {
            echo "Missing bundle license file: $bundle_file (must be packed via mygo.json \"resources\" before codesign)" >&2
            return 1
        }
        [ -s "$bundle_file" ] || {
            echo "Bundle license file is empty: $bundle_file" >&2
            return 1
        }
        cmp -s "$repo_file" "$bundle_file" || {
            echo "Bundle license file differs from repository copy: $bundle_file vs $repo_file (regenerate THIRD_PARTY_NOTICES or rebuild)" >&2
            return 1
        }
    done

    PLIST_JSON=$plist_json python3 - "$REPO_ROOT/mygo.json" "$APP_PATH" <<'PY' || return 1
import json, os, sys

cfg_path, app_path = sys.argv[1:3]
with open(cfg_path, "r", encoding="utf-8") as f:
    cfg = json.load(f)

try:
    plist = json.loads(os.environ.get("PLIST_JSON", ""))
except Exception as exc:
    sys.stderr.write(f"Failed to decode Info.plist JSON for {app_path}: {exc}\n")
    sys.exit(1)

if not isinstance(plist, dict):
    sys.stderr.write(f"Invalid Info.plist root object for {app_path}\n")
    sys.exit(1)

errors = []
expected_name = cfg["name"]
expected_id = cfg["identifier"]
expected_version = cfg["version"]
expected_schemes = cfg.get("urlSchemes") or []
expected_min_sys = ""
macos_cfg = cfg.get("macos")
if isinstance(macos_cfg, dict) and isinstance(macos_cfg.get("minimumSystemVersion"), str):
    expected_min_sys = macos_cfg["minimumSystemVersion"].strip()

def check_exact(key, expected):
    val = plist.get(key)
    if not isinstance(val, str) or val != expected:
        errors.append(f"Info.plist {key}: got {val!r}, expected {expected!r}.")

check_exact("CFBundleIdentifier", expected_id)
check_exact("CFBundleName", expected_name)
check_exact("CFBundleDisplayName", expected_name)
check_exact("CFBundleExecutable", expected_name)

for ver_key in ("CFBundleShortVersionString", "CFBundleVersion"):
    val = plist.get(ver_key)
    if not isinstance(val, str) or not val.strip():
        errors.append(f"Info.plist {ver_key} must be a non-empty string.")
    elif val != expected_version:
        errors.append(f"Info.plist {ver_key}: got {val!r}, expected {expected_version!r} (from mygo.json).")

check_exact("CFBundleIconFile", "AppIcon.icns")

icon_name_expected = ""
macos_icon_cfg = ""
if isinstance(macos_cfg, dict) and isinstance(macos_cfg.get("icon"), str):
    macos_icon_cfg = macos_cfg["icon"]
if macos_icon_cfg.lower().endswith((".icon", ".car")):
    icon_name_expected = "AppIcon"
    override = None
    if isinstance(macos_cfg, dict):
        info_plist_cfg = macos_cfg.get("infoPlist")
        if isinstance(info_plist_cfg, dict) and isinstance(info_plist_cfg.get("CFBundleIconName"), str):
            override = info_plist_cfg["CFBundleIconName"]
    if override is not None:
        icon_name_expected = override
    val = plist.get("CFBundleIconName")
    if not isinstance(val, str) or val != icon_name_expected:
        errors.append(f"Info.plist CFBundleIconName: got {val!r}, expected {icon_name_expected!r}.")

min_sys = plist.get("LSMinimumSystemVersion")
if not isinstance(min_sys, str) or not min_sys.strip():
    errors.append("Info.plist LSMinimumSystemVersion must be a non-empty string.")
elif expected_min_sys and min_sys != expected_min_sys:
    errors.append(f"Info.plist LSMinimumSystemVersion: got {min_sys!r}, expected {expected_min_sys!r}.")

found_schemes = []
url_types = plist.get("CFBundleURLTypes")
if isinstance(url_types, list):
    for entry in url_types:
        if isinstance(entry, dict):
            schemes = entry.get("CFBundleURLSchemes")
            if isinstance(schemes, list):
                for s in schemes:
                    if isinstance(s, str):
                        found_schemes.append(s)

for scheme in expected_schemes:
    if scheme not in found_schemes:
        errors.append(f"Info.plist CFBundleURLTypes is missing expected URL scheme {scheme!r} (found: {found_schemes!r}).")

if errors:
    sys.stderr.write(f"Bundle Info.plist verification failed for {app_path}:\n")
    for err in errors:
        sys.stderr.write(f"  - {err}\n")
    sys.exit(1)

print(f"Bundle & Info.plist verified: {app_path}")
print(f"  CFBundleIdentifier: {plist['CFBundleIdentifier']}")
print(f"  CFBundleName / DisplayName / Executable: {plist['CFBundleName']} / {plist['CFBundleDisplayName']} / {plist['CFBundleExecutable']}")
print(f"  CFBundleShortVersionString / CFBundleVersion: {plist['CFBundleShortVersionString']} / {plist['CFBundleVersion']}")
print(f"  CFBundleIconFile: {plist['CFBundleIconFile']}")
print(f"  CFBundleIconName: {plist.get('CFBundleIconName', '(absent)')}")
print(f"  LSMinimumSystemVersion: {plist['LSMinimumSystemVersion']}")
print(f"  CFBundleURLSchemes: {', '.join(found_schemes) if found_schemes else '(none)'}")
PY

    printf 'License notices: LICENSE, THIRD_PARTY_NOTICES (verified)\n'
}

cmd_verify() {
    [ -d "$APP_PATH" ] || {
        echo "App bundle not found: $APP_PATH" >&2
        return 1
    }
    verify_codesign || return 1
    verify_bundle_and_plist || return 1
}

validate_build_timeout() {
    BUILD_TIMEOUT=${BRANCHLINE_BUILD_TIMEOUT:-180}
    python3 - "$BUILD_TIMEOUT" <<'PY'
import math, sys

raw = sys.argv[1]
try:
    val = float(raw)
    if not math.isfinite(val) or val <= 0:
        raise ValueError("must be > 0")
except ValueError:
    sys.stderr.write(
        f"Invalid BRANCHLINE_BUILD_TIMEOUT={raw!r}: expected a positive number of seconds.\n"
    )
    sys.exit(2)
PY
}

run_build_with_timeout() {
    python3 - "$BUILD_TIMEOUT" "$@" <<'PY'
import os, signal, subprocess, sys, time

timeout_raw = sys.argv[1]
timeout = float(timeout_raw)
cmd = sys.argv[2:]

proc = subprocess.Popen(cmd, start_new_session=True)
pgid = proc.pid

def group_alive(gid):
    try:
        os.killpg(gid, 0)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        return True

def terminate_group(gid, child):
    try:
        os.killpg(gid, signal.SIGTERM)
    except (ProcessLookupError, PermissionError):
        pass
    deadline = time.monotonic() + 2.0
    while time.monotonic() < deadline:
        child_done = child.poll() is not None
        if child_done and not group_alive(gid):
            break
        time.sleep(0.05)
    if child.poll() is None or group_alive(gid):
        try:
            os.killpg(gid, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            pass
        kill_deadline = time.monotonic() + 2.0
        while time.monotonic() < kill_deadline:
            child_done = child.poll() is not None
            if child_done and not group_alive(gid):
                break
            time.sleep(0.05)
    try:
        child.wait(timeout=2.0)
    except subprocess.TimeoutExpired:
        pass

def forward_signal(signum, _frame):
    terminate_group(pgid, proc)
    sys.exit(128 + signum)

signal.signal(signal.SIGTERM, forward_signal)
signal.signal(signal.SIGINT, forward_signal)

try:
    rc = proc.wait(timeout=timeout)
except subprocess.TimeoutExpired:
    terminate_group(pgid, proc)
    sys.stderr.write(
        f"\nBuild/signing timed out after {timeout_raw}s; terminated the build process group.\n"
        "Non-interactive troubleshooting:\n"
        "  1. When codesign reads the login keychain private key, a locked keychain or unauthorized key ACL\n"
        "     causes macOS to show a GUI authorization dialog on the local Mac screen and block silently\n"
        "     (mygo v0.10.0 logs a warning after 15s but does not exit).\n"
        "  2. Check whether the login keychain is unlocked and whether a codesign prompt is waiting on screen.\n"
        "  3. Check network reachability to Apple's timestamp server (used by codesign --timestamp).\n"
        "  4. Adjust BRANCHLINE_BUILD_TIMEOUT if a slower clean compile legitimately needs more time.\n"
        "This script did NOT modify keychain/ACL settings and did NOT fall back to ad-hoc signing.\n"
    )
    sys.exit(124)

sys.exit(rc)
PY
}

cmd_build() {
    validate_build_timeout
    cmd_check
    build_status=0
    run_build_with_timeout go tool mygo build -platform "darwin/$ARCH" -sign "$IDENTITY" -skip-dmg || build_status=$?
    if [ "$build_status" -ne 0 ]; then
        if [ -e "$APP_PATH" ]; then
            rm -rf "$APP_PATH"
            echo "Removed unverified bundle after build failure: $APP_PATH" >&2
        fi
        exit "$build_status"
    fi

    verify_status=0
    cmd_verify || verify_status=$?
    if [ "$verify_status" -ne 0 ]; then
        if [ -e "$APP_PATH" ]; then
            rm -rf "$APP_PATH"
            echo "Removed unverified bundle after verification failure: $APP_PATH" >&2
        fi
        exit "$verify_status"
    fi
}

load_mygo_config
resolve_identity

case "$MODE" in
    check) cmd_check ;;
    build) cmd_build ;;
    verify) cmd_verify ;;
esac
