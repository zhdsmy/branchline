# macOS development signing

How Branchline signs its local builds with a persistent certificate, why it does so, and how to set up the same on your Mac.

## Why persistent signing is required

Building without a signing identity — `go tool mygo build -sign -` — produces an ad-hoc signature. The designated requirement of an ad-hoc-signed app is anchored on the `cdhash` of the exact binary, so it changes on every build:

```sh
codesign -dr - build/darwin-arm64/Branchline.app
# designated => identifier "com.zhdsmy.branchline" and cdhash H"…"   ← changes on every compile
```

macOS privacy permissions (TCC), such as access to the Documents folder, are granted to a code identity. When the identity changes on every rebuild, the system no longer matches the previously granted permission and prompts again after every recompile.

Signing with a persistent certificate instead keeps the designated requirement stable across builds:

```text
identifier "com.zhdsmy.branchline" and certificate leaf = H"<sha1>"
```

With a fixed identity, a TCC permission granted once survives recompiles. Branchline therefore never falls back to ad-hoc signing: `scripts/package-macos.sh` refuses `"-"` and requires a valid certificate identity before it builds anything.

## Create a local self-signed code-signing certificate

You can skip this section if you already have an Apple Development or Developer ID certificate in your keychain — those work as-is.

1. Open **Keychain Access**.
2. In the menu bar, choose **Keychain Access → Certificate Assistant → Create a Certificate…**
3. Enter a name, e.g. `Branchline Local Development`.
4. Set **Identity Type** to **Self Signed Root** and **Certificate Type** to **Code Signing**, then click **Create**. The certificate and its private key are stored in your login keychain.
5. Trust the certificate for code signing: in Keychain Access, double-click the newly created certificate, expand **Trust**, set **Code Signing** to **Always Trust**, and close the window (authenticate when prompted to save the change). Without this step, `security find-identity -v -p codesigning` will not list the self-signed certificate as valid.

> **Privacy note:** The certificate's subject fields (name, email address, country, etc.) are embedded in the code signature and can be read by anyone who inspects a signed app. Avoid putting personal information in the certificate if you plan to share signed builds.

The scripts never create certificates, modify keychains, or change trust settings. Creating and trusting the certificate is a one-time step you do yourself. On the first build with this certificate, macOS displays a keychain prompt asking whether `codesign` may use the private key; click **Always Allow** so subsequent builds run non-interactively.

List your valid code-signing identities (read-only) and copy the 40-character SHA-1 of the one you want to use:

```sh
security find-identity -v -p codesigning
#   1) <40-char-sha1> "Branchline Local Development"
#      1 valid identities found
```

## Configure the signing identity

The signing identity is the 40-character SHA-1 fingerprint of the certificate. It is resolved in this order:

1. The `BRANCHLINE_SIGN_IDENTITY` environment variable:
   ```sh
   export BRANCHLINE_SIGN_IDENTITY='<40-char-sha1>'
   ```
2. The file `.mygo/macos-signing-identity` in the current checkout (git-ignored; it contains only the public SHA-1, never a private key).
3. In a linked git worktree: the file `.mygo/macos-signing-identity` of the main checkout, located through `git rev-parse --git-common-dir`.

Resolution fails closed:

- An explicitly empty value, `"-"`, a non-40-hexadecimal value, or a SHA-1 with no valid identity in the keychain aborts before any build — the scripts never silently fall back to ad-hoc signing.
- A present but empty or invalid `.mygo/macos-signing-identity` in a worktree fails closed instead of falling back to the main checkout.
- Never write a certificate SHA-1 into `mygo.json` or any other version-controlled file.

## Build, verify, and dev commands

```sh
# Check identity configuration and print the target bundle path
sh scripts/macos.sh check

# Build a signed bundle and verify it (no GUI is launched)
sh scripts/macos.sh build

# Re-verify an existing bundle
sh scripts/macos.sh verify

# Continuous development mode (interactive)
sh scripts/macos.sh dev
```

`check`, `build` and `verify` are thin delegates of `scripts/package-macos.sh`; `dev` validates the identity first and then runs `go tool mygo dev -sign <identity>`.

- `build` runs `go tool mygo build -platform "darwin/<arch>" -sign "<identity>" -skip-dmg` and produces `build/darwin-<arch>/Branchline.app` (`build/darwin-arm64/Branchline.app` on Apple silicon), then verifies the bundle automatically. A failed build or verification removes the unverified `.app`.
- `verify` checks:
  - `codesign --verify --deep --strict -R '=certificate leaf = H"<sha1>"'`;
  - `codesign -dv --verbose=4`: rejects `Signature=adhoc` and the `adhoc` code flag; the `Identifier` must match `mygo.json`; a non-empty `Authority` must exist; the Hardened Runtime (`runtime`) flag must be set; `TeamIdentifier` is printed (`not set` for a self-signed certificate, or a 10-character Apple Team ID);
  - `codesign -dr -`: the designated requirement must lock `identifier "com.zhdsmy.branchline"` and the lowercase leaf certificate fingerprint, and must not pin a `cdhash`;
  - `plutil -lint` plus Info.plist and bundle checks driven by `mygo.json` as the single source of truth: bundle identifier, names, executable, versions, `CFBundleIconFile`/`CFBundleIconName`, the `branchline` URL scheme, minimum system version, an executable `Contents/MacOS/Branchline`, a non-empty `AppIcon.icns` and `Assets.car`, and the license notices `Contents/Resources/LICENSE` and `Contents/Resources/THIRD_PARTY_NOTICES` (present, non-empty, and byte-identical to the repository root copies).

### Build timeouts and keychain prompts

When `codesign` reads the private key, a locked login keychain or a key ACL that does not cover `codesign` makes macOS show a GUI authorization dialog on the local screen and block silently (on the first build, click **Always Allow** to grant `codesign` persistent access). Unreachable network access to Apple's timestamp server (`codesign --timestamp`) can also stall signing. `scripts/package-macos.sh build` therefore wraps the build in a bounded timeout (default `BRANCHLINE_BUILD_TIMEOUT=180` seconds). On timeout it terminates the stuck build process group, removes the partial `.app`, exits `124`, and prints troubleshooting guidance. It never modifies keychain or ACL settings and never downgrades to ad-hoc signing. If a clean compile legitimately needs more time, raise the timeout:

```sh
BRANCHLINE_BUILD_TIMEOUT=600 sh scripts/macos.sh build
```

## Dev mode identity vs. release identity

Continuous development mode builds a separate app — `Branchline Dev` with bundle ID `com.zhdsmy.branchline.dev` — while `build` produces `com.zhdsmy.branchline`. macOS treats them as distinct applications, so each may ask for its own permissions the first time it touches a protected directory. Use one mode consistently when checking permission behavior across rebuilds.

## Local development vs. public distribution

A local self-signed certificate shows `TeamIdentifier=not set`. It is suitable for local development and for a self-signed alpha build shared with instructions to open it manually. It is **not** an Apple Developer ID: it does not carry Gatekeeper trust, and the app is not notarized. Public distribution with a Developer ID certificate and notarization is a separate process and is not covered by these scripts.

## Offline tests

`sh scripts/package-macos_test.sh` runs the hermetic unit tests of the packaging scripts. It uses temporary workspaces with fake `security`/`codesign`/`go` stubs: it never touches a real keychain, never signs, and never launches any GUI.
