# Publishing the npm package

The npm version must equal the GitHub release version without its `v` prefix.
For example, npm `0.1.1` installs artifacts from GitHub Release `v0.1.1`.

1. Update the version in this directory's `package.json` and `package-lock.json`:

   ```bash
   npm version <version> --no-git-tag-version
   ```

2. Commit the change and create the matching Git tag, for example `v0.1.1`.
3. Run `goreleaser release --clean` from the repository root and confirm that
   the GitHub Release contains all platform archives and `checksums.txt`.
4. From this directory, run `npm install`, `npm test`, and
   `npm run pack:dry-run`.
5. Authenticate with npm and publish:

   ```bash
   npm publish --access public
   ```

6. In an empty temporary directory, verify the published package:

   ```bash
   npm install -g @softpractice/softpractice-cli@<version>
   softpractice --help
   ```

Do not publish the npm package before its matching GitHub Release exists.
