"use strict";

// Temporary backport of https://github.com/electron-userland/electron-builder/pull/10101, commit
// 7abb30e393326676237862163a115c96e2f0e80d, plus partition-list log redaction.
// Remove only when a released dependency passes the installed-module regressions.
const fs = require("node:fs");
const path = require("node:path");
const { createHash } = require("node:crypto");
const targets = require("../patches/signing-backport.json");
const fixedTargets = {
  "app-builder-lib": "out/codeSign/macCodeSign.js",
  "builder-util": "out/util.js",
};

function refuse() {
  // Never include source bytes, process output or underlying filesystem errors.
  return new Error("Signing backport dependency drift or incomplete installation; reinstall the pinned dependencies");
}

function sha256(bytes) {
  return createHash("sha256").update(bytes).digest("hex");
}

function applyBackport(desktopRoot) {
  try {
    if (targets.length !== Object.keys(fixedTargets).length || new Set(targets.map(t => t.module)).size !== targets.length) throw refuse();
    const states = targets.map(target => {
      if (!Object.hasOwn(fixedTargets, target.module) || target.file !== fixedTargets[target.module]) throw refuse();
      const moduleRoot = path.join(desktopRoot, "node_modules", target.module);
      if (JSON.parse(fs.readFileSync(path.join(moduleRoot, "package.json"), "utf8")).version !== target.version) throw refuse();
      const file = path.join(moduleRoot, target.file);
      const bytes = fs.readFileSync(file);
      const digest = sha256(bytes);
      const kind = digest === target.originalSha256 ? "original" : digest === target.patchedSha256 ? "patched" : null;
      if (kind === null) throw refuse();
      return { target, file, bytes, kind };
    });
    if (states.every(state => state.kind === "patched")) return;
    if (!states.every(state => state.kind === "original")) throw refuse();

    // Validate and prepare EVERY target before writing any of them.
    const prepared = states.map(({ target, file, bytes }) => {
      let patched = bytes.toString("utf8");
      for (const { before, after } of target.replacements) {
        if (before.length === 0 || patched.split(before).length !== 2) throw refuse();
        patched = patched.replace(before, () => after);
      }
      if (sha256(patched) !== target.patchedSha256) throw refuse();
      return { file, bytes: patched };
    });
    for (const item of prepared) fs.writeFileSync(item.file, item.bytes);
  } catch {
    throw refuse();
  }
}

module.exports = { applyBackport };

if (require.main === module) {
  try {
    applyBackport(path.resolve(__dirname, ".."));
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
