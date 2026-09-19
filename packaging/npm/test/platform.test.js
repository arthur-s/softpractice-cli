"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { releaseArtifact } = require("../lib/platform");

test("maps GoReleaser archive names for supported platforms", () => {
  assert.deepEqual(releaseArtifact("darwin", "arm64"), {
    filename: "softpractice-cli_Darwin_arm64.tar.gz",
    executable: "softpractice",
  });
  assert.deepEqual(releaseArtifact("linux", "x64"), {
    filename: "softpractice-cli_Linux_x86_64.tar.gz",
    executable: "softpractice",
  });
  assert.deepEqual(releaseArtifact("win32", "x64"), {
    filename: "softpractice-cli_Windows_x86_64.zip",
    executable: "softpractice.exe",
  });
});

test("rejects an unsupported platform", () => {
  assert.throws(() => releaseArtifact("freebsd", "x64"), /unsupported platform/);
});
