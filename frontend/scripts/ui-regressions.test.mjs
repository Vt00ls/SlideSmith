import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";


const appSource = await readFile(new URL("../src/App.tsx", import.meta.url), "utf8");
const stylesSource = await readFile(new URL("../src/styles.css", import.meta.url), "utf8");


test("source profile renders only a non-empty trimmed string", () => {
  assert.match(
    appSource,
    /typeof sourceContract\?\.source_profile === "string"\s*&&\s*sourceContract\.source_profile\.trim\(\) !== ""/,
  );
  assert.match(appSource, /\? sourceContract\.source_profile\.trim\(\)\s*:\s*"-"/);
});


test("upload helper text is bounded and wraps unbroken filenames", () => {
  const rule = stylesSource.match(/\.upload-zone small\s*\{([^}]*)\}/s);
  assert.ok(rule, "missing dedicated .upload-zone small rule");
  assert.match(rule[1], /max-width:\s*100%\s*;/);
  assert.match(rule[1], /overflow-wrap:\s*anywhere\s*;/);
});
