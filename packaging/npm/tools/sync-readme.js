"use strict";

const fs = require("node:fs");
const path = require("node:path");

const packageRoot = path.resolve(__dirname, "..");
const repositoryRoot = path.resolve(packageRoot, "..", "..");

fs.copyFileSync(
  path.join(repositoryRoot, "README.md"),
  path.join(packageRoot, "README.md"),
);
