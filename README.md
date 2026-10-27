# purl2swhid

Convert a Package URL into a Software Heritage identifier. Downloads the package archive, hashes its unpacked contents, and optionally checks whether that identifier is archived or requests preservation through Save Code Now.

## Install

```sh
go install github.com/git-pkgs/purl2swhid/cmd/purl2swhid@latest
```

Requires Go 1.27.3 or later. To build from a checkout, run `make build`; the binary is written to `bin/purl2swhid`.

## Quick start

```sh
purl2swhid pkg:npm/is-number@7.0.0
```

Output:

```text
swh:1:dir:fb9b2897efee5fb381d1258543fff68114c92ece
```

The default output is one SWHID followed by a newline. `--json` includes the normalized PURL, archive URL, filename, selected root, and any preservation results.

```sh
purl2swhid pkg:npm/is-number --json
purl2swhid pkg:gem/rake@13.2.1 --json
purl2swhid pkg:npm/is-number@7.0.0 --check
purl2swhid 'pkg:npm/is-number@7.0.0#index.js'
purl2swhid pkg:pypi/six@1.17.0 --filename six-1.17.0-py2.py3-none-any.whl
```

Omitting the version selects the latest release using `git-pkgs/registries`, and `--json` reports the resolved version in the PURL. The resolver uses the registry's advertised latest version when available, otherwise the latest active release by publication time or ecosystem version ordering. Pin a version for repeatable results.

Registry and archive support comes from the git-pkgs modules; unsupported packages return an error. The `repository_url` qualifier selects a registry. PyPI uses a source distribution by default, while `--filename` selects a specific artifact. Other ecosystems use the registry resolver's default artifact.

## Commands

```text
purl2swhid <package-url>       download and hash a package
purl2swhid completion <shell> generate shell completion scripts
purl2swhid --version          print the version
```

Options:

```text
    --json               output JSON with package, archive and preservation details
    --check              check whether the SWHID is in Software Heritage
    --save               check and request archival if the SWHID is missing
    --filename string    select an exact artifact filename
    --keep-root          keep the archive wrapper directory
    --swh-url string     use another Software Heritage deployment
    --timeout duration   timeout for the entire operation (default 5m)
-h, --help               show help
-v, --version            print the version
```

## Archive handling

The directory hash includes file names, contents, executable bits, symlink targets, and empty directories. npm, Cargo, and PyPI source distributions remove one shared wrapper directory when present. PyPI artifacts marked as binary distributions keep their root, including ZIP and egg files selected with `--filename`. Go packages select the `module@version` directory and treat regular files as non-executable, following Go's [module ZIP metadata rules](https://go.dev/ref/mod#zip-files). RubyGems and Hex hash their inner package contents; other formats keep the archive root. `--keep-root` retains the wrapper, and a PURL subpath selects a directory or file within the chosen root. Selecting a file returns a content SWHID.

Repeated directory entries are ignored, and the last file entry for a path wins. File/directory conflicts, missing hard links, special files, and paths deeper than 128 components return errors. Files without Unix mode metadata also return errors, except for Go modules and compressed single-file payloads. Downloads, individual files, and total expanded contents are each limited to 512 MiB, with at most 100,000 archive entries. Hashing uses archive streams without extracting files to disk; ZIP archives require buffering their compressed contents. A compressed payload without a TAR container is buffered within the file limit and hashed as a non-executable member named `content`. `--timeout` limits the entire operation and defaults to five minutes.

## Software Heritage

`--check` looks up the computed SWHID in Software Heritage. Plain output keeps the SWHID on stdout and reports preservation status on stderr; JSON includes a `known` boolean. An absent identifier is a successful lookup with `known: false`. If a check or save request fails, the computed result is still printed to stdout, the error goes to stderr, and the command exits with status 1. JSON omits `known` when the lookup could not establish preservation status.

```sh
purl2swhid pkg:npm/is-number@7.0.0 --save --json
```

`--save` checks first, then submits the public HTTPS archive URL to Save Code Now with visit type `tarball` if the identifier is absent. Accepted or pending requests include their ID and status in the result. Rejected requests fail. Submission is asynchronous: it does not mean the package has been archived, and this CLI does not poll for completion. Software Heritage's loader may interpret an archive differently or lack support for a package format. A locally computed directory SWHID can also differ from the package's source repository.

Set `SWH_TOKEN` to a Software Heritage offline token for authenticated requests to the default archive. `--swh-url` selects another archive deployment and disables use of `SWH_TOKEN`. Downloaded contents are hashed as received; registry checksums and signatures are not verified.

## How it works

- [git-pkgs/purl](https://github.com/git-pkgs/purl) parses and normalizes Package URLs.
- [git-pkgs/registries](https://github.com/git-pkgs/registries) fetches metadata, resolves artifact URLs, and downloads packages.
- [git-pkgs/archives](https://github.com/git-pkgs/archives) reads archive entries and their file metadata.
- [andrew/swhid-go](https://github.com/andrew/swhid-go) computes content and directory SWHIDs with SHA-1 collision detection.
- [andrew/swh-go](https://github.com/andrew/swh-go) handles Software Heritage lookups, authentication, and save requests.

[Cobra](https://github.com/spf13/cobra) provides the command-line interface. Archive handling also draws on the Go implementation in [swhid-wasm](https://github.com/andrew/swhid-wasm).

## Exit codes

```text
0  hashing and any requested preservation operations completed
1  invalid input, download or hashing failure, or preservation request failure
```

A successful lookup reporting `known: false` exits with status 0. A failed preservation request exits with status 1 while retaining the computed SWHID on stdout.

## Development

```sh
make build
make test
make lint
```

Tests exercise the Cobra command with registry and Software Heritage responses supplied in-process. No test submits a real save request.

## License

[MIT](LICENSE).
