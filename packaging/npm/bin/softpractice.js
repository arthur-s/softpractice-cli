#!/usr/bin/env node

"use strict";

const { existsSync } = require("node:fs");
const { spawnSync } = require("node:child_process");
const { binaryPath } = require("../lib/platform");

const executable = binaryPath();

if (!existsSync(executable)) {
  console.error(
    "softpractice: the native binary is missing. Reinstall @softpractice/softpractice-cli without --ignore-scripts.",
  );
  process.exit(1);
}

const result = spawnSync(executable, process.argv.slice(2), { stdio: "inherit" });
if (result.error) {
  console.error(`softpractice: could not start native binary: ${result.error.message}`);
  process.exit(1);
}
process.exit(result.status === null ? 1 : result.status);
