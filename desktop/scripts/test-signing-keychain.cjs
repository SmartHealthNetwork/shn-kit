"use strict";

// Real macOS characterization of the upstream temporary-keychain password fix:
// https://github.com/electron-userland/electron-builder/pull/10101
// Synthetic certificates only. This is explicitly outside the hermetic test gate.
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { createHash, randomBytes } = require("node:crypto");
const { spawn, spawnSync } = require("node:child_process");

function check(ok, detail) {
  if (!ok) throw new Error(`keychain regression ${detail}`);
}

function command(file, args, env) {
  const result = spawnSync(file, args, { encoding: "utf8", timeout: 15_000, killSignal: "SIGKILL", maxBuffer: 1024 * 1024, env });
  // Raw subprocess errors include arguments. Never disclose them.
  check(!result.error && result.status === 0, "setup or cleanup command rejected");
  return result.stdout;
}

function security(args) {
  return command("/usr/bin/security", args, { PATH: "/usr/bin:/bin", LANG: "C", LC_ALL: "C" });
}

function parseSearchList(output) {
  return output.split("\n").map(line => line.trim()).filter(Boolean).map(line => {
    check(/^"[^"\r\n]+"$/.test(line), "search-list format is unsupported");
    return line.slice(1, -1);
  });
}

// The supervisor owns this cleanup before launching the worker. In particular,
// createKeychain rejection happens before electron-builder registers disposal.
async function withKeychainCleanup({ keychain, searchList, security: run = security, exists = fs.existsSync }, body) {
  let value, failed = false, cleanupFailed = false;
  try { value = await body(); } catch (error) {
    // A live worker may still be using or creating the keychain. Retain its
    // directory and search-list state rather than racing it with cleanup.
    if (error.retainOwnedMaterial) throw error;
    failed = true;
  }
  // Independent attempts: deletion failure must not suppress restoration, nor
  // should restoration failure leave an owned keychain undeleted.
  try {
    if (exists(keychain) || exists(`${keychain}-db`)) run(["delete-keychain", keychain]);
  } catch { cleanupFailed = true; }
  try { run(["list-keychains", "-d", "user", "-s", ...searchList]); } catch { cleanupFailed = true; }
  try {
    check(JSON.stringify(parseSearchList(run(["list-keychains", "-d", "user"]))) === JSON.stringify(searchList), "search list was not restored");
  } catch { cleanupFailed = true; }
  check(!cleanupFailed, "cleanup failed; owned temporary keychain needs inspection");
  check(!failed, "scenario failed (worker output withheld)");
  return value;
}

// A separate process group lets the supervisor kill and reap a stalled worker
// and its security subprocess before touching the keychain or search list.
function retainedWorkerFailure() {
  return Object.assign(new Error("keychain regression worker quiescence unverified; owned material retained"), { retainOwnedMaterial: true });
}

function groupExists(pid) {
  try { process.kill(-pid, 0); return true; } catch (error) {
    if (error.code === "ESRCH") return false;
    throw error;
  }
}

function runWorker(file, args, { timeout = 90_000, env, signal, terminateGroup, groupExists: probe = groupExists, quiescenceTimeout = 2000 } = {}) {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) { reject(new Error("keychain regression worker cancelled")); return; }
    const child = spawn(file, args, { detached: process.platform !== "win32", stdio: ["ignore", "pipe", "pipe"], env });
    let output = "", size = 0, timedOut = false, oversized = false, cancelled = false;
    const killGroup = () => {
      try {
        if (terminateGroup) terminateGroup(child.pid);
        else process.platform === "win32" ? child.kill("SIGKILL") : process.kill(-child.pid, "SIGKILL");
      } catch (error) {
        if (error.code !== "ESRCH") {
          clearTimeout(timer);
          child.unref();
          child.stdout.destroy();
          child.stderr.destroy();
          reject(retainedWorkerFailure());
        }
      }
    };
    const timer = setTimeout(() => { timedOut = true; killGroup(); }, timeout);
    const abort = () => { cancelled = true; killGroup(); };
    signal?.addEventListener("abort", abort, { once: true });
    child.stdout.on("data", chunk => {
      size += chunk.length;
      if (size > 1024 * 1024) { oversized = true; killGroup(); } else output += chunk;
    });
    child.stderr.on("data", chunk => {
      size += chunk.length;
      if (size > 1024 * 1024) { oversized = true; killGroup(); }
    });
    child.on("error", () => { clearTimeout(timer); reject(new Error("keychain regression worker could not start")); });
    child.on("exit", killGroup);
    child.on("close", async code => {
      clearTimeout(timer);
      signal?.removeEventListener("abort", abort);
      // execFile's subprocess pipes are not the worker's stdout/stderr. Its
      // close event alone cannot prove that every killed descendant has exited.
      if (child.pid && process.platform !== "win32") {
        const deadline = Date.now() + quiescenceTimeout;
        try {
          while (probe(child.pid)) {
            if (Date.now() >= deadline) { reject(retainedWorkerFailure()); return; }
            await new Promise(resolve => setTimeout(resolve, 10));
          }
        } catch { reject(retainedWorkerFailure()); return; }
      }
      if (cancelled) reject(new Error("keychain regression worker cancelled"));
      else if (timedOut) reject(new Error("keychain regression worker timed out"));
      else if (oversized || code !== 0) reject(new Error("keychain regression worker rejected"));
      else resolve(output);
    });
  });
}

function createCancellation() {
  const controller = new AbortController();
  let exitCode = 0;
  const cancel = name => {
    if (!controller.signal.aborted) {
      exitCode = name === "SIGINT" ? 130 : 143;
      controller.abort();
    }
  };
  const interrupt = () => cancel("SIGINT");
  const terminate = () => cancel("SIGTERM");
  process.on("SIGINT", interrupt);
  process.on("SIGTERM", terminate);
  return {
    signal: controller.signal,
    get exitCode() { return exitCode; },
    dispose() { process.removeListener("SIGINT", interrupt); process.removeListener("SIGTERM", terminate); },
  };
}

function verifyInstalledBackport() {
  const root = path.resolve(__dirname, "..");
  for (const target of require("../patches/signing-backport.json")) {
    const moduleRoot = path.join(root, "node_modules", target.module);
    check(JSON.parse(fs.readFileSync(path.join(moduleRoot, "package.json"), "utf8")).version === target.version, "installed dependency version differs");
    const digest = createHash("sha256").update(fs.readFileSync(path.join(moduleRoot, target.file))).digest("hex");
    check(digest === target.patchedSha256, "installed backport is missing; run npm ci");
  }
}

const scenarioNames = ["original-password", "patched-password", "wrong-import-password", "wrong-keychain-password", "partition-command-failure"];

async function scenario(name, root) {
  check(scenarioNames.includes(name), "unknown scenario");
  verifyInstalledBackport();
  const certificatePassword = process.env.SYNTHETIC_CERT_PASSWORD;
  const wrongPassword = process.env.SYNTHETIC_WRONG_PASSWORD;
  check(certificatePassword && wrongPassword && certificatePassword !== wrongPassword, "synthetic inputs missing");
  const keychain = path.join(root, `${createHash("sha256").update(root).update("app-builder").digest("hex")}.keychain`);
  const builderUtil = require("builder-util");
  const logging = require("builder-util/out/log");
  const originalExec = builderUtil.exec;
  const captured = [];
  const secrets = [certificatePassword, wrongPassword];
  const commands = [];
  logging.debug.enabled = true;
  logging.shouldDisableNonErrorLoggingVitest = false;
  logging.log = new logging.Logger({ write: text => captured.push(text) });
  let generatedPassword, locked = false, failureStage, failureText = "";
  async function realSecurity(args) {
    try {
      return await originalExec("/usr/bin/security", args, { timeout: 15_000, killSignal: "SIGKILL", env: { PATH: "/usr/bin:/bin", LANG: "C", LC_ALL: "C" } });
    } catch (error) {
      logging.log.error(error);
      throw error;
    }
  }
  builderUtil.exec = async (file, input) => {
    check(file === "/usr/bin/security", "unexpected executable");
    const args = [...input];
    const verb = args[0];
    check(["delete-keychain", "create-keychain", "unlock-keychain", "set-keychain-settings", "list-keychains", "import", "set-key-partition-list"].includes(verb), "unexpected security operation");
    if (verb !== "list-keychains") {
      const target = verb === "import" ? args[args.indexOf("-k") + 1] : args.at(-1);
      check(target === keychain, "security operation escaped owned keychain");
    } else if (args.includes("-s")) {
      check(args[4] === keychain && JSON.stringify(args.slice(5)) === process.env.SYNTHETIC_SEARCH_LIST, "search-list update escaped owned keychain");
    }
    commands.push(verb);
    if (verb === "create-keychain") {
      generatedPassword = args[2];
      secrets.push(generatedPassword);
      check(generatedPassword !== certificatePassword && generatedPassword !== wrongPassword, "passwords must differ");
    }
    if (verb === "set-key-partition-list") {
      check(args[args.indexOf("-k") + 1] === generatedPassword, "installed module passed the wrong password");
      await realSecurity(["lock-keychain", keychain]);
      locked = true;
      // Negative control changes only this invocation, never installed bytes.
      if (name === "original-password") args[args.indexOf("-k") + 1] = certificatePassword;
      if (name === "wrong-keychain-password") args[args.indexOf("-k") + 1] = wrongPassword;
      if (name === "partition-command-failure") args.push("--synthetic-partition-failure");
    }
    try { return await realSecurity(args); } catch (error) {
      if (verb !== "delete-keychain") { failureStage = verb; failureText = String(error.message); }
      throw error;
    }
  };
  let result, rejected = false;
  try {
    result = await require("app-builder-lib/out/codeSign/macCodeSign").createKeychain({
      tmpDir: {}, currentDir: root, cscLink: path.join(root, "synthetic.p12"),
      cscKeyPassword: name === "wrong-import-password" ? wrongPassword : certificatePassword,
    });
  } catch { rejected = true; }
  const text = captured.join("");
  check(secrets.every(secret => !text.includes(secret) && !failureText.includes(secret)), "credential appeared in installed debug/error logging");
  check(text.includes("executing") && text.includes("args=import "), "real debug logging was not exercised");
  check(commands.filter(c => c === "import").length === 1, "certificate import was not exercised once");
  if (name === "patched-password") {
    check(!rejected && result.keychainFile === keychain && locked, "corrected locked-keychain path failed");
  } else {
    check(rejected && text.includes("Exit code:"), "failure did not reject through installed error logging");
    if (name === "wrong-import-password") check(failureStage === "import" && !locked && !commands.includes("set-key-partition-list"), "wrong import password did not stop before partition");
    else {
      check(failureStage === "set-key-partition-list" && locked, "locked partition failure was not exercised");
      if (name !== "partition-command-failure") check(/SecKeychainUnlock/.test(failureText), "wrong password did not produce SecKeychainUnlock rejection");
    }
  }
  process.stdout.write(JSON.stringify({ scenario: name, accepted: true, outcome: rejected ? "rejected" : "succeeded", locked, redaction: "verified", ...(name === "original-password" ? { reason: "SecKeychainUnlock" } : {}) }));
}

async function main() {
  check(process.platform === "darwin", "requires macOS (run the hermetic npm test on other platforms)");
  verifyInstalledBackport();
  const searchList = parseSearchList(security(["list-keychains", "-d", "user"]));
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "shn-signing-regression-"));
  fs.chmodSync(root, 0o700);
  const certificatePassword = randomBytes(24).toString("hex");
  const wrongPassword = randomBytes(24).toString("hex");
  const env = { PATH: "/usr/bin:/bin", LANG: "C", LC_ALL: "C", SYNTHETIC_CERT_PASSWORD: certificatePassword, SYNTHETIC_WRONG_PASSWORD: wrongPassword, SYNTHETIC_SEARCH_LIST: JSON.stringify(searchList) };
  const cancellation = createCancellation();
  let cleanupVerified = false, retainRoot = false;
  try {
    command("/usr/bin/openssl", ["req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", path.join(root, "synthetic.key"), "-out", path.join(root, "synthetic.crt"), "-days", "1", "-subj", "/CN=Synthetic Keychain Regression"], env);
    command("/usr/bin/openssl", ["pkcs12", "-export", "-inkey", path.join(root, "synthetic.key"), "-in", path.join(root, "synthetic.crt"), "-out", path.join(root, "synthetic.p12"), "-passout", "env:SYNTHETIC_CERT_PASSWORD", "-keypbe", "PBE-SHA1-3DES", "-certpbe", "PBE-SHA1-3DES", "-macalg", "sha1"], env);
    for (const name of scenarioNames) {
      await new Promise(resolve => setImmediate(resolve));
      check(!cancellation.signal.aborted, "cancelled");
      const keychain = path.join(root, `${createHash("sha256").update(root).update("app-builder").digest("hex")}.keychain`);
      cleanupVerified = false;
      let report;
      try {
        report = await withKeychainCleanup({ keychain, searchList }, () => runWorker(process.execPath, [__filename, "--scenario", name, root], { signal: cancellation.signal, env: { ...env, TRAVIS: "true", APP_BUILDER_TMP_DIR: root } }));
      } catch (error) {
        retainRoot = error.retainOwnedMaterial === true;
        throw error;
      } finally {
        // Do not remove the containing directory if a keychain still survives.
        if (!retainRoot) cleanupVerified = !fs.existsSync(keychain) && !fs.existsSync(`${keychain}-db`) && JSON.stringify(parseSearchList(security(["list-keychains", "-d", "user"]))) === JSON.stringify(searchList);
      }
      check(cleanupVerified, "owned keychain still exists after cleanup");
      const parsed = JSON.parse(report);
      check(parsed.scenario === name && parsed.accepted === true, "worker result was incomplete");
      console.log(`PASS ${name}: ${parsed.outcome}${parsed.reason ? ` (${parsed.reason})` : ""}; debug/error redaction verified; search list restored; owned keychain deleted`);
    }
  } finally {
    // Only this mkdtemp-created directory is eligible. Keep it on failed keychain
    // cleanup or unverified worker termination. SIGKILL/machine failure cannot run finally.
    const ownedKeychain = path.join(root, `${createHash("sha256").update(root).update("app-builder").digest("hex")}.keychain`);
    try {
      if (!retainRoot && !fs.existsSync(ownedKeychain) && !fs.existsSync(`${ownedKeychain}-db`)) fs.rmSync(root, { recursive: true, force: true });
      else console.error(`keychain regression retained owned temporary directory: ${root}`);
    } finally {
      if (cancellation.exitCode) process.exitCode = cancellation.exitCode;
      cancellation.dispose();
    }
  }
  check(!fs.existsSync(root), "temporary directory was not removed");
  verifyInstalledBackport();
  console.log("PASS installed backport unchanged; temporary synthetic material removed");
}

module.exports = { withKeychainCleanup, runWorker, createCancellation };
if (require.main === module) {
  const work = process.argv[2] === "--scenario" ? scenario(process.argv[3], process.argv[4]) : main();
  work.catch(error => {
    // Only our fixed messages are safe. Node/OS errors can contain credentials.
    console.error(/^keychain regression [a-zA-Z0-9 ();/.-]+$/.test(error.message) ? error.message : "keychain regression failed; raw output withheld");
    process.exitCode ||= 1;
  });
}
