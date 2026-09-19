"use strict";

const crypto = require("node:crypto");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const AdmZip = require("adm-zip");
const tar = require("tar");
const { releaseArtifact } = require("../lib/platform");

const packageRoot = path.resolve(__dirname, "..");
const packageInfo = require(path.join(packageRoot, "package.json"));
const repository = "https://github.com/arthur-s/softpractice-cli";

async function download(url) {
  const response = await fetch(url);
  if (!response.ok) {
    throw new Error(`download ${url}: HTTP ${response.status}`);
  }
  return Buffer.from(await response.arrayBuffer());
}

function expectedChecksum(checksums, filename) {
  const escapedFilename = filename.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = checksums.match(new RegExp(`^([a-fA-F0-9]{64})\\s+\\*?${escapedFilename}$`, "m"));
  if (!match) {
    throw new Error(`checksum for ${filename} is absent from checksums.txt`);
  }
  return match[1].toLowerCase();
}

async function extract(archive, artifact, directory) {
  const archivePath = path.join(directory, artifact.filename);
  await fs.writeFile(archivePath, archive);
  if (artifact.filename.endsWith(".zip")) {
    new AdmZip(archivePath).extractAllTo(directory, true);
    return;
  }
  await tar.x({ file: archivePath, cwd: directory, strict: true });
}

async function install() {
  const artifact = releaseArtifact();
  const tag = `v${packageInfo.version}`;
  const releaseURL = `${repository}/releases/download/${tag}`;
  const [archive, checksums] = await Promise.all([
    download(`${releaseURL}/${artifact.filename}`),
    download(`${releaseURL}/checksums.txt`),
  ]);
  const actualChecksum = crypto.createHash("sha256").update(archive).digest("hex");
  if (actualChecksum !== expectedChecksum(checksums.toString("utf8"), artifact.filename)) {
    throw new Error(`checksum mismatch for ${artifact.filename}`);
  }

  const temporaryDirectory = await fs.mkdtemp(path.join(os.tmpdir(), "softpractice-npm-"));
  try {
    await extract(archive, artifact, temporaryDirectory);
    const source = path.join(temporaryDirectory, artifact.executable);
    const destinationDirectory = path.join(packageRoot, "dist");
    const destination = path.join(destinationDirectory, artifact.executable);
    await fs.access(source);
    await fs.rm(destinationDirectory, { recursive: true, force: true });
    await fs.mkdir(destinationDirectory, { recursive: true });
    await fs.copyFile(source, destination);
    if (process.platform !== "win32") {
      await fs.chmod(destination, 0o755);
    }
    console.log(`Installed Softpractice CLI ${packageInfo.version} for ${process.platform}/${process.arch}.`);
  } finally {
    await fs.rm(temporaryDirectory, { recursive: true, force: true });
  }
}

install().catch((error) => {
  console.error(`softpractice install: ${error.message}`);
  process.exitCode = 1;
});
