# Softpractice CLI npm installer

This package is a launcher for the native Softpractice CLI. During installation
it downloads the GoReleaser archive with the same version from the GitHub
Release, verifies it against `checksums.txt`, and installs the `softpractice`
command.

```bash
npm install -g @softpractice/softpractice-cli
softpractice --help
```

The installer supports macOS, Linux, and Windows on `arm64` and `x64`. It
requires Node.js 18 or newer and access to GitHub Releases. Installation with
`--ignore-scripts` is unsupported because npm would not download the native
binary.

## Manual release

The npm version must equal the GitHub release version without its `v` prefix.
For example, npm `0.1.0` installs artifacts from GitHub Release `v0.1.0`.

1. Update the `version` in this directory's `package.json`.
2. Commit it and create the matching Git tag, for example `v0.1.0`.
3. Run `goreleaser release --clean` from the repository root and confirm that
   the GitHub Release contains all platform archives and `checksums.txt`.
4. From this directory, run `npm install`, `npm test`, and `npm pack --dry-run`.
5. Authenticate once with `npm login`, then publish with:

   ```bash
   npm publish --access public
   ```

6. In an empty temporary directory, verify the published package:

   ```bash
   npm install -g @softpractice/softpractice-cli@0.1.0
   softpractice --help
   ```

Do not publish the npm package before its matching GitHub Release exists.
