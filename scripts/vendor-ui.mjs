import {
  copyFile,
  mkdtemp,
  rename,
  rm,
} from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const repositoryRoot = fileURLToPath(new URL("../", import.meta.url));
const destination = join(repositoryRoot, "internal", "ui", "assets", "vendor");
const staging = await mkdtemp(join(dirname(destination), ".vendor-"));

const files = [
  {
    source: "node_modules/chart.js/dist/chart.umd.min.js",
    destination: "chart.umd.min.js",
  },
  {
    source: "node_modules/chart.js/LICENSE.md",
    destination: "chart.js.LICENSE.md",
  },
  {
    source: "node_modules/@highlightjs/cdn-assets/highlight.min.js",
    destination: "highlight.min.js",
  },
  {
    source: "node_modules/@highlightjs/cdn-assets/styles/github.min.css",
    destination: "github.min.css",
  },
  {
    source: "node_modules/@highlightjs/cdn-assets/styles/github-dark.min.css",
    destination: "github-dark.min.css",
  },
  {
    source: "node_modules/@highlightjs/cdn-assets/LICENSE",
    destination: "highlight.js.LICENSE",
  },
];

try {
  for (const file of files) {
    await copyFile(
      join(repositoryRoot, file.source),
      join(staging, file.destination),
    );
  }

  await rm(destination, { recursive: true, force: true });
  await rename(staging, destination);
} catch (error) {
  await rm(staging, { recursive: true, force: true });
  throw error;
}

console.log(`Prepared ${files.length} embedded UI assets`);
