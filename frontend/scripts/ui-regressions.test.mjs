import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";


const appSource = await readFile(new URL("../src/App.tsx", import.meta.url), "utf8");
const stylesSource = await readFile(new URL("../src/styles.css", import.meta.url), "utf8");


async function loadRetryOptionsModule() {
  const sourceFile = ts.createSourceFile("App.tsx", appSource, ts.ScriptTarget.ES2020, true, ts.ScriptKind.TSX);
  let splitRetryOptions = "";
  let retryOptionsForFailure = "";
  sourceFile.forEachChild((node) => {
    if (ts.isVariableStatement(node) && node.declarationList.declarations.some(
      (declaration) => ts.isIdentifier(declaration.name) && declaration.name.text === "splitRetryOptions",
    )) {
      splitRetryOptions = node.getText(sourceFile);
    }
    if (ts.isFunctionDeclaration(node) && node.name?.text === "retryOptionsForFailure") {
      retryOptionsForFailure = node.getText(sourceFile);
    }
  });
  assert.ok(splitRetryOptions, "missing splitRetryOptions declaration");
  assert.ok(retryOptionsForFailure, "missing retryOptionsForFailure declaration");
  const source = `${splitRetryOptions}\n${retryOptionsForFailure}\nexport { retryOptionsForFailure };`;
  const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 },
  }).outputText;
  return import(`data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}`);
}


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


test("template resolve failure offers only prepare retry", async () => {
  const { retryOptionsForFailure } = await loadRetryOptionsModule();
  assert.deepEqual(retryOptionsForFailure("template_resolve"), [
    { phase: "prepare", label: "重新准备" },
  ]);
});
