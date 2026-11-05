# Licenses of what you ship

This is engineering practice, not legal advice. For a commercial product with copyleft components, ask a lawyer.

## Three questions, in this order

1. **What is compiled into each build?** Not what is in `go.mod`: what `go list -deps` finds for that operating system, architecture and set of build tags. A Windows build and a macOS build contain different modules.
2. **What does each license ask of whoever hands the program on?** Permissive licenses (MIT, BSD, Apache-2.0) ask that their text, and for Apache-2.0 any NOTICE file, travel with the binary. File-level copyleft (MPL-2.0) and library copyleft (LGPL) have conditions on the files or the linking. GPL and AGPL have conditions on the whole work, and AGPL on network use. A few components add clauses of their own: naming and branding, attribution in the interface, restrictions on use.
3. **Does what you ship meet them?** The text travels with the program, the product's name and description are in order, and the source is offered where that is required.

## Notices that travel with the program

`assets/go-notices/` is a generic generator (about 270 lines, no dependencies beyond the standard library, with tests).

```sh
mkdir -p scripts/notices
cp .agents/skills/go-release-quality-gates/assets/go-notices/*.go scripts/notices/
go run ./scripts/notices -product "My App" -goos windows -goarch amd64,arm64 \
    -tags "$RELEASE_TAGS" -out THIRD-PARTY-NOTICES.txt ./cmd/...
```

| Flag | Meaning |
|---|---|
| `-product` | name used in the introduction (required) |
| `-goos`, `-goarch` | target system, and architectures separated by commas; a universal macOS build passes both (required) |
| `-tags` | the same build tags as the build, or the list differs from what is shipped |
| `-cgo` | list as a build with cgo enabled (off by default) |
| `-note` | repeatable text added after the introduction, for what a build carries that is no Go module and so is in no list the tool can read (a driver, a font, a binary blob), and for statements about an embedded component: its license and where its source is |
| `-out` | the file to write (required) |
| packages | default `./...` |

Behaviour worth knowing:

- It lists the modules of the packages and their dependencies for the target, skips the main module, adds the Go standard library's license, and **groups identical license texts** so the GPL is printed once with every module that carries it.
- **A module with no license file in its root stops the run.** Somebody has to read what it requires before a build goes out; shipping it unmentioned is worse. Read the repository (the license may sit in a subdirectory or only in the README), then replace the dependency, get permission, or record the decision with `-note`. Never silence the stop.
- It runs offline when the modules are in the module cache. It does not support vendor mode (`GOFLAGS=-mod=mod`).
- Output is deterministic, so a diff shows exactly what a dependency change did to the notices.

Wire it in:

- Run it in the packaging scripts, once per product build, writing into the bundle (`Contents/Resources` on macOS, next to the executable and into the installer on Windows). A file nobody can find has not "travelled with" the program, so let the interface open it (Settings, About, Licenses) and test that the file is present in the built package.
- Run its tests in CI (`go test ./scripts/notices`), and rerun it after any dependency upgrade: upgrades change licenses too.
- Do not hand-edit the generated file.
- The generator covers Go modules. Fonts, icons, images, sounds and anything else under your resources directory need their licenses recorded separately (a short `resources/LICENSES.md` is enough).

## Reviewing a component with unusual terms

For an embedded engine, codec or library that the product is built around, before the design is fixed:

- **Read the license text itself**, not the badge: which license and which version (GPL-2.0 versus 3.0, "or later"), and whether anything is added to the standard text.
- **Look for additional clauses.** A naming clause (derivative works must not use the name or imply an association) changes what the product may be called and how its README may describe it. An attribution clause changes the interface.
- **Decide what embedding means for your own code.** Linking a copyleft component into your binary makes the whole program a work that must be distributed under compatible terms, with the corresponding source available. Running it as a separate process changes the analysis but is not an automatic exemption. Decide this before design, not at release.
- **Record the outcome in the repository:** a license file for the product itself, and a "Licensing" section in the README that names the embedded component, its version, its license, any naming clause and how the product complies, and where the corresponding source can be obtained.
- **Say what the product is.** An unofficial product built on a component says so ("based on", "unofficial"), and uses its own name and icon.
- **Carry the license text and a source pointer** (tag or commit) in the notices with `-note`, and mention the license in the release notes.

An engine's license can also decide which build tags and features may ship: check each optional feature that links more code before turning it on.

## Evidence for the final report

State, per build: the notices file was generated for the shipped tags and platforms; it is inside the package (list the package contents to show it); no module lacked a license; the licensing section of the README is current. Anything a lawyer should look at goes in the "open decision" column of the completion audit (`long-horizon-delivery`).
