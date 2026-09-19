"use strict";

const path = require("node:path");

const platforms = {
  darwin: "Darwin",
  linux: "Linux",
  win32: "Windows",
};

const architectures = {
  arm64: "arm64",
  x64: "x86_64",
};

function releaseArtifact(platform = process.platform, architecture = process.arch) {
  const os = platforms[platform];
  const arch = architectures[architecture];
  if (!os || !arch) {
    throw new Error(`unsupported platform: ${platform}/${architecture}`);
  }

  const extension = platform === "win32" ? "zip" : "tar.gz";
  return {
    filename: `softpractice-cli_${os}_${arch}.${extension}`,
    executable: platform === "win32" ? "softpractice.exe" : "softpractice",
  };
}

function binaryPath(packageRoot = path.resolve(__dirname, "..")) {
  return path.join(packageRoot, "dist", releaseArtifact().executable);
}

module.exports = { binaryPath, releaseArtifact };
