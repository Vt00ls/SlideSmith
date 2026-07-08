#!/usr/bin/env node

const { spawn } = require("node:child_process");
const path = require("node:path");

function logRuntimeMessage(message, payload = {}) {
  try {
    const { runtime } = require("@chaitin-ai/agent-compose-runtime-sdk");
    if (runtime && typeof runtime.log === "function") {
      runtime.log(message, payload);
    }
  } catch {
    // The runtime SDK is helpful but not required for local script execution.
  }
}

function main() {
  const runner = path.resolve(__dirname, "../scripts/ppt_runner.py");
  const args = process.argv.slice(2);
  logRuntimeMessage("slidesmith ppt workflow started", { args });

  const child = spawn("python3", [runner, ...args], {
    cwd: path.resolve(__dirname, ".."),
    env: {
      ...process.env,
      PYTHONUNBUFFERED: "1",
    },
    stdio: "inherit",
  });

  child.on("exit", (code, signal) => {
    if (signal) {
      console.error(`ppt workflow terminated by signal ${signal}`);
      process.exit(1);
      return;
    }
    process.exit(code ?? 1);
  });
}

main();

