# Packaging and releasing without installing

How to build installers for a desktop application with a privileged helper, check them, and publish them, without running them on the machine that built them. The shapes below come from a macOS `.pkg` and a Windows NSIS installer built from one Go repository; adapt the names.

## Principles

- **One script per platform**, runnable by a developer and by CI, that writes only inside its own output directory (`build/<platform>`, ignored by git) and removes only what it makes. `rm -rf "$out"` as a whole is how a script deletes someone's work.
- **No privilege to build.** Building needs no root and no administrator. If a build step seems to need it, the design is wrong.
- **One source of truth for the version** (the framework's config file or a `VERSION` file). The script refuses a release version that names another one, because the metadata of the app comes from the config and a mismatch ships two versions in one product.
- **Stamp every binary built with the same version and commit** (`-ldflags "-s -w -X pkg/version.Version=$v -X pkg/version.Commit=$c"`, plus `-trimpath` and `CGO_ENABLED=0` where cgo is not needed). The interface and the service then report the same build, and a mismatch is a bug the interface can show.
- **Do not install what you build.** Read it with the tools that read packages (below).
- **Generate files that must agree with code from the code.** The launchd property list was written by the service's own `print-plist` command, so the label, program path and flags in it cannot drift from the constants in the program.

## macOS: bundle with a root helper, built as a `.pkg`

Order of the script (`build.sh --arch arm64|x86_64|universal`):

1. Build any native library (a Swift or C bridge) for each architecture and join them with `lipo -create`.
2. Build the service for each architecture (`CGO_ENABLED=0 GOOS=darwin GOARCH=...`) and `lipo -create` them into one file.
3. Write the launchd property list by running the service built for the host architecture, then `plutil -lint` it.
4. Build the application bundle with the framework's builder (for MyGo: `go tool mygo build -platform darwin/universal -skip-dmg -skip-build-command -sign "$identity" -o DIR`).
5. Copy the service, the library and the plist into the bundle (`Contents/MacOS`, `Contents/Frameworks`, `Contents/Library/LaunchDaemons`), add the third-party notices to `Contents/Resources`, then **`xattr -cr`** the bundle (quarantine and Finder metadata do not belong in a signature or an installer, which would carry them as `._` files; a mark that some systems put on files written by tracked programs, `com.apple.provenance`, cannot be removed this way and is harmless to the signature).
6. Sign **from the inside out**: the library, then the service under its own identifier (`codesign -i <identifier>`, which is what the interface asks for when it connects), then the bundle. Verify with `codesign --verify --strict --deep`.
7. Build the installer package.

Signing identities:

| | flags |
|---|---|
| Ad hoc (`-`) | `--force --timestamp=none`. Good to build, try and test in CI. Not good to hand to anybody: Gatekeeper and the approval of a launchd service expect a Developer ID signature |
| Developer ID | `--force --timestamp --options runtime` (hardened runtime, needed for notarization), plus a Developer ID Installer identity for the package (`productbuild --sign`) |

Notarization and stapling are not covered by anything above and need a Developer ID and the notary service; treat them as "documented, not verified" until someone runs them with real credentials.

The package:

```sh
pkgbuild --analyze --root "$stage" component.plist
plutil -replace 0.BundleIsRelocatable -bool false component.plist
pkgbuild --root "$stage" --install-location /Applications --ownership recommended \
  --component-plist component.plist --identifier "$app_id.pkg" --version "$version" \
  --scripts "$scripts" component.pkg
sed "s/@VERSION@/$version/g" distribution.xml > distribution-out.xml
productbuild --distribution distribution-out.xml --package-path . "MyApp-$version.pkg"
```

- **Non-relocatable** (`BundleIsRelocatable` false). Otherwise the installer may "update" another copy of the bundle it finds on the disk instead of `/Applications`. A root service that refuses to run from anywhere but a place only administrators can write to must be installed exactly there.
- `--ownership recommended` installs the files owned by root, which is what lets the service trust them.
- The `--scripts` directory carries `preinstall` and `postinstall`; `xattr -cr` it too and shellcheck it.

Inspect without installing (read-only; the expanded copy goes in a temporary directory):

```sh
pkgutil --check-signature X.pkg          # "no signature" for an ad hoc build, the identity otherwise
pkgutil --payload-files X.pkg | head     # what would be installed, and where
tmp="$(mktemp -d)"; pkgutil --expand X.pkg "$tmp/expanded"; ls "$tmp/expanded"   # Distribution, component.pkg (Bom, PackageInfo, Payload, Scripts)
xar -tf X.pkg                            # the archive's members
codesign --verify --strict --deep "App.app"; codesign -dv "App.app" 2>&1 | grep -E '^(Identifier|Signature|Format)'
lipo -archs "App.app/Contents/MacOS/service"   # arm64 x86_64 for a universal build
plutil -lint LaunchDaemon.plist
```

`._*` AppleDouble entries in `--payload-files` are what `pkgbuild` makes of leftover extended attributes: harmless, and minimized by `xattr -cr`.

## Windows: NSIS from any operating system

- Go compiles for Windows without cgo, and most frameworks can build the Windows executable from another OS, so the installer is built on a Linux or macOS runner (`apt-get install nsis`, `brew install makensis`). On a Windows runner install with `choco install nsis -y`, add `C:\Program Files (x86)\NSIS` to `PATH` and use `shell: bash` for the script.
- Pass everything the installer needs on the command line: `makensis -V2 -DVERSION=... -DVERSION_NUMERIC=... -DARCH=... -DAGENT=... -DGUI=... -DNOTICES=... -DOUTFILE=... installer.nsi`. The script fails with `!error` when `ARCH` is missing.
- NSIS wants a four-number product version: derive `VERSION_NUMERIC` with `sed -E 's/^([0-9]+)\.([0-9]+)\.([0-9]+).*/\1.\2.\3.0/'`, which also strips a suffix such as `-rc1`.
- Build one installer per architecture (`amd64`, `arm64`); name them with the version and architecture.
- Skip the framework's own per-user installer when the product installs for the whole computer (`RequestExecutionLevel admin`, `SetShellVarContext all`).
- Unsigned unless you have an Authenticode certificate: the first run shows a SmartScreen warning. Document it.
- Inspect without running: `file X.exe` shows "Nullsoft Installer self-extracting archive". 7-Zip can list the members of an NSIS installer when it is available; this was not exercised.

## The release workflow

Trigger on a version tag. Per platform job: build, upload the artifact. A last job downloads them all and publishes.

```yaml
on:
  push:
    tags: ['v*']
permissions:
  contents: write
```

- **Secrets as environment, conditions on the environment.** A step cannot ask whether a secret exists, only whether an environment variable is non-empty, so map the secrets in `env:` and write `if: env.CERT != ''`. Without the secrets the build still runs and signs ad hoc (macOS) or not at all (Windows), which is how forks and trial runs behave.
- **Empty is not unset.** A secret that does not exist reaches the job as an empty variable. A script that defaults with `${X-default}` (no colon) or tests `${X+set}` treats it as set, and would sign with an identity named "". Default with `${X:-default}` in the script and unset the empty ones in the workflow (`[ -n "$X" ] || unset X`), so both sides agree.
- **Import the certificate into a throwaway keychain** on the runner: `security create-keychain`, `set-keychain-settings -lut 21600`, `unlock-keychain`, `security import cert.p12 -P ... -A -t cert -f pkcs12 -k keychain`, `set-key-partition-list -S apple-tool:,apple: -s -k ...` (so `codesign` does not prompt), `list-keychains -d user -s keychain login.keychain-db`, then delete the `.p12`. The keychain password is a fresh `uuidgen` and nothing is echoed.
- **Version from the tag**: `export VERSION="${GITHUB_REF_NAME#v}"`, which the script checks against the config.
- **Checksums**: `sha256sum -- * > SHA256SUMS` in the directory of artifacts (`--` so a name that starts with `-` is not read as an option).
- **Publish**: `gh release create "$GITHUB_REF_NAME" dist/* --repo "$GITHUB_REPOSITORY" --title "$GITHUB_REF_NAME" --generate-notes` with `GH_TOKEN: ${{ github.token }}`.

## Rehearse on every push

Run the same packaging scripts, unsigned, in the regular CI (a `macos` job that builds the universal package, a `windows` job that builds an installer) and upload the results as artifacts. Release day then has no first-time failures, and a change that breaks packaging fails the pull request that made it.

## What only a real machine can verify

Say so in the README, with a checklist a maintainer can follow (install, approve the service, connect, disconnect, upgrade over the previous version, uninstall, reboot): the install itself, the approval of a launchd service, driver loading, UAC prompts, notarization and Gatekeeper, SmartScreen, an upgrade over an older version. Mark each "documented, not verified" in the final report rather than implying it was exercised.
