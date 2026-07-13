import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";

const appSource = await readFile(new URL("../src/App.tsx", import.meta.url), "utf8");
const apiSource = await readFile(new URL("../src/api.ts", import.meta.url), "utf8");
const formatSource = await readFile(new URL("../src/format.ts", import.meta.url), "utf8");
const routerSource = await readFile(new URL("../src/router.ts", import.meta.url), "utf8");
const stylesSource = await readFile(new URL("../src/styles.css", import.meta.url), "utf8");

async function loadSourceModule(source, fileName) {
  const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 },
    fileName,
  }).outputText;
  return import(`data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}`);
}

async function loadAppHelpersModule() {
  const sourceFile = ts.createSourceFile("App.tsx", appSource, ts.ScriptTarget.ES2020, true, ts.ScriptKind.TSX);
  const variableNames = new Set([
    "activeStatuses",
    "confirmationStatuses",
    "splitRetryOptions",
    "templateFillPlanInputRecoveryNote",
    "templateFillPlanStatuses",
  ]);
  const functionNames = new Set([
    "isConfirmationStatus",
    "isWaitingStatus",
    "numberFromSummary",
    "templateFillText",
    "templateFillSlideRows",
    "templateFillCheckRows",
    "templateFillActionState",
    "retryOptionsForFailure",
    "retryGuidanceForFailure",
    "canOpenTemplateFillPlan",
    "completedTaskRoute",
    "visibleTaskArtifacts",
  ]);
  const declarations = [];
  const found = new Set();
  sourceFile.forEachChild((node) => {
    if (ts.isVariableStatement(node)) {
      const matches = node.declarationList.declarations
        .filter((declaration) => ts.isIdentifier(declaration.name) && variableNames.has(declaration.name.text))
        .map((declaration) => declaration.name.text);
      if (matches.length > 0) {
        declarations.push(node.getText(sourceFile));
        matches.forEach((name) => found.add(name));
      }
    }
    if (ts.isFunctionDeclaration(node) && node.name && functionNames.has(node.name.text)) {
      declarations.push(node.getText(sourceFile));
      found.add(node.name.text);
    }
  });
  const missing = [...variableNames, ...functionNames].filter((name) => !found.has(name));
  assert.deepEqual(missing, [], `missing production helper declarations: ${missing.join(", ")}`);
  return loadSourceModule(`${declarations.join("\n")}\nexport { ${[...variableNames, ...functionNames].join(", ")} };`, "App.tsx");
}

function appFunctionSource(name) {
  const sourceFile = ts.createSourceFile("App.tsx", appSource, ts.ScriptTarget.ES2020, true, ts.ScriptKind.TSX);
  let result = "";
  sourceFile.forEachChild((node) => {
    if (ts.isFunctionDeclaration(node) && node.name?.text === name) {
      result = node.getText(sourceFile);
    }
  });
  assert.ok(result, `missing ${name} declaration`);
  return result;
}

function typeAliasSource(source, fileName, name) {
  const sourceFile = ts.createSourceFile(fileName, source, ts.ScriptTarget.ES2020, true, ts.ScriptKind.TS);
  let result = "";
  sourceFile.forEachChild((node) => {
    if (ts.isTypeAliasDeclaration(node) && node.name.text === name) {
      result = node.getText(sourceFile);
    }
  });
  assert.ok(result, `missing ${name} type`);
  return result;
}

function mediaQuerySource(maxWidth) {
  const marker = `@media (max-width: ${maxWidth}px)`;
  const start = stylesSource.indexOf(marker);
  assert.notEqual(start, -1, `missing ${maxWidth}px media query`);
  const next = stylesSource.indexOf("@media ", start + marker.length);
  return stylesSource.slice(start, next === -1 ? undefined : next);
}

test("Template Fill API surface includes every status, phase, type field, and endpoint", () => {
  const taskStatus = typeAliasSource(apiSource, "api.ts", "TaskStatus");
  const pipelinePhase = typeAliasSource(apiSource, "api.ts", "PipelinePhase");
  const retryPhase = typeAliasSource(apiSource, "api.ts", "RetryPhase");
  for (const status of [
    "template_fill_planning",
    "awaiting_template_fill_confirm",
    "template_fill_checking",
    "template_fill_applying",
    "template_fill_validating",
  ]) {
    assert.match(taskStatus, new RegExp(`\\| \\"${status}\\"`));
  }
  for (const phase of ["template_fill_plan", "template_fill_check", "template_fill_apply", "template_fill_validate"]) {
    assert.match(pipelinePhase, new RegExp(`\\| \\"${phase}\\"`));
    assert.match(retryPhase, new RegExp(`\\| \\"${phase}\\"`));
  }
  const templateFillInputs = typeAliasSource(apiSource, "api.ts", "TemplateFillInputs");
  for (const field of [
    "project_path", "source_pptx", "slide_library", "fill_plan", "check_report", "validate_report",
    "readback", "export_base", "content_sources",
  ]) {
    assert.match(templateFillInputs, new RegExp(`\\b${field}:`), `missing TemplateFillInputs.${field}`);
  }
  const templateFillPlanFile = typeAliasSource(apiSource, "api.ts", "TemplateFillPlanFile");
  for (const field of ["name", "path", "content", "size", "updated_at"]) {
    assert.match(templateFillPlanFile, new RegExp(`\\b${field}:`), `missing TemplateFillPlanFile.${field}`);
  }
  const templateFillPlanPreview = typeAliasSource(apiSource, "api.ts", "TemplateFillPlanPreview");
  for (const field of [
    "task_id", "project_path", "inputs", "plan", "check_report", "summary", "plan_file", "can_edit", "can_confirm",
  ]) {
    assert.match(templateFillPlanPreview, new RegExp(`\\b${field}:`), `missing TemplateFillPlanPreview.${field}`);
  }
  for (const method of [
    "getTemplateFillPlan", "saveTemplateFillPlan", "checkTemplateFillPlan",
    "confirmTemplateFillPlan", "regenerateTemplateFillPlan",
  ]) {
    assert.match(apiSource, new RegExp(`\\b${method}:`), `missing API method ${method}`);
  }
  assert.match(apiSource, /\/template-fill\/plan/);
  assert.match(apiSource, /\/template-fill\/check/);
  assert.match(apiSource, /\/template-fill\/confirm/);
  assert.match(apiSource, /\/template-fill\/regenerate/);
  assert.match(apiSource, /saveTemplateFillPlan:[\s\S]*?method:\s*"PUT"[\s\S]*?JSON\.stringify\(\{ plan \}\)/);
  for (const method of ["checkTemplateFillPlan", "confirmTemplateFillPlan", "regenerateTemplateFillPlan"]) {
    const declaration = apiSource.match(new RegExp(`${method}:[\\s\\S]*?(?=\\n  [a-zA-Z]|\\n};)`))?.[0] || "";
    assert.match(declaration, /method:\s*"POST"/, `${method} must POST`);
  }
});

test("Template Fill router parses and serializes the plan hash", async () => {
  globalThis.window = { location: { hash: "#/tasks/task%20one/template-fill" } };
  const { parseRoute, routeToHash } = await loadSourceModule(routerSource, "router.ts");
  assert.deepEqual(parseRoute(), { name: "templateFill", id: "task%20one" });
  assert.equal(routeToHash({ name: "templateFill", id: "task%20one" }), "#/tasks/task%20one/template-fill");
  window.location.hash = routeToHash({ name: "templateFill", id: "task-2" });
  assert.deepEqual(parseRoute(), { name: "templateFill", id: "task-2" });
});

test("Template Fill labels and status classifications are exact", async () => {
  const [{ statusLabel, statusTone, phaseLabel, artifactKindLabel }, helpers] = await Promise.all([
    loadSourceModule(formatSource, "format.ts"),
    loadAppHelpersModule(),
  ]);
  assert.deepEqual(
    [
      "template_fill_planning",
      "awaiting_template_fill_confirm",
      "template_fill_checking",
      "template_fill_applying",
      "template_fill_validating",
    ].map((status) => [statusLabel[status], statusTone[status]]),
    [
      ["生成填充计划", "active"],
      ["审查填充计划", "waiting"],
      ["检查填充计划", "active"],
      ["填充 PPTX", "active"],
      ["校验填充结果", "active"],
    ],
  );
  assert.deepEqual(
    ["template_fill_plan", "template_fill_check", "template_fill_apply", "template_fill_validate"].map((phase) => phaseLabel[phase]),
    ["填充计划", "计划检查", "PPTX 填充", "结果校验"],
  );
  assert.deepEqual(
    ["template_fill_plan", "template_fill_check_report", "template_fill_validate_report", "template_fill_readback"].map(
      (kind) => artifactKindLabel[kind],
    ),
    ["填充计划", "计划检查报告", "填充校验报告", "填充回读文本"],
  );
  assert.ok(helpers.activeStatuses.includes("template_fill_planning"));
  assert.ok(helpers.activeStatuses.includes("template_fill_checking"));
  assert.ok(helpers.activeStatuses.includes("template_fill_applying"));
  assert.ok(helpers.activeStatuses.includes("template_fill_validating"));
  assert.equal(helpers.isWaitingStatus("awaiting_template_fill_confirm"), true);
  assert.equal(helpers.isConfirmationStatus("awaiting_template_fill_confirm"), false);
});

test("slide rows preserve rationale, notes presence, and edit counts while tolerating malformed values", async () => {
  const { templateFillSlideRows } = await loadAppHelpersModule();
  const rows = templateFillSlideRows({
    slides: [
      {
        source_slide: 3,
        purpose: "结论页",
        layout_rationale: { layout_pattern: "hero", why_fit: "突出核心结论", risk: "标题可能过长" },
        notes: "  保留品牌页脚  ",
        replacements: [{}, {}],
        table_edits: [{}],
        chart_edits: [{}, {}, {}],
      },
      { source_slide: null, purpose: undefined, layout_rationale: "bad", notes: "   ", replacements: {}, table_edits: null },
      null,
    ],
  });
  assert.deepEqual(rows[0], {
    index: 1,
    sourceSlide: "3",
    purpose: "结论页",
    layoutPattern: "hero",
    whyFit: "突出核心结论",
    risk: "标题可能过长",
    notes: "有",
    replacements: 2,
    tableEdits: 1,
    chartEdits: 3,
  });
  assert.deepEqual(rows[1], {
    index: 2,
    sourceSlide: "-",
    purpose: "-",
    layoutPattern: "-",
    whyFit: "-",
    risk: "-",
    notes: "无",
    replacements: 0,
    tableEdits: 0,
    chartEdits: 0,
  });
  assert.equal(rows[2].purpose, "-");
  assert.equal(rows[2].notes, "无");
  assert.deepEqual(templateFillSlideRows({ slides: "bad" }), []);
});

test("check rows tolerate unknown data, filter OK, and sort ERROR before WARN stably", async () => {
  const { templateFillCheckRows } = await loadAppHelpersModule();
  assert.deepEqual(templateFillCheckRows({ results: "bad" }), []);
  const rows = templateFillCheckRows({
    results: [
      { status: "warn", code: "source_reuse_concentration", source_slide: 4, message: "重复使用较多" },
      { status: "OK", code: "fine" },
      null,
      { status: "error", code: "missing", plan_slide: 2, message: "缺少内容" },
      { status: "WARN", code: null, plan_slide: null, source_slide: undefined, message: null },
      { status: "ERROR", code: "later", plan_slide: 3 },
    ],
  });
  assert.deepEqual(rows.map((row) => row.status), ["ERROR", "ERROR", "WARN", "WARN"]);
  assert.deepEqual(rows.map((row) => row.code), ["missing", "later", "source_reuse_concentration", "-"]);
  assert.deepEqual(rows[2], {
    status: "WARN",
    code: "source_reuse_concentration",
    planSlide: "-",
    sourceSlide: "4",
    message: "重复使用较多",
  });
  assert.deepEqual(rows[3], { status: "WARN", code: "-", planSlide: "-", sourceSlide: "-", message: "-" });
});

test("dirty and check-error action guards never act on stale JSON", async () => {
  const { templateFillActionState } = await loadAppHelpersModule();
  const base = {
    canEdit: true,
    canConfirm: true,
    taskStatus: "awaiting_template_fill_confirm",
    busy: false,
    dirty: false,
    checkErrorCount: 0,
  };
  assert.deepEqual(templateFillActionState({ ...base, dirty: true }), {
    saveDisabled: false,
    checkDisabled: true,
    confirmDisabled: true,
    hint: "JSON 已修改，请先保存后再检查或确认。",
  });
  assert.equal(templateFillActionState({ ...base, canConfirm: false }).confirmDisabled, true);
  assert.deepEqual(templateFillActionState({ ...base, checkErrorCount: 2 }), {
    saveDisabled: false,
    checkDisabled: false,
    confirmDisabled: true,
    hint: "存在 2 个检查错误，请修正并保存后再确认。",
  });
  assert.equal(templateFillActionState(base).confirmDisabled, false, "a saved draft without a report can confirm");
  assert.equal(templateFillActionState({ ...base, checkWarningCount: 9 }).confirmDisabled, false, "warnings do not block confirm");
});

test("Template Fill retry recovery is failure-phase-aware and main retry behavior remains intact", async () => {
  const { retryOptionsForFailure, retryGuidanceForFailure } = await loadAppHelpersModule();
  assert.deepEqual(retryOptionsForFailure("template_fill_plan.inputs", "template-fill"), [
    { phase: "prepare", label: "重新准备" },
    { phase: "template_fill_plan", label: "重建填充计划" },
  ]);
  assert.deepEqual(retryOptionsForFailure("template_fill_plan.contract", "template-fill"), [
    { phase: "template_fill_plan", label: "重建填充计划" },
  ]);
  assert.deepEqual(retryOptionsForFailure("template_fill_check.command", "template-fill"), [
    { phase: "template_fill_check", label: "重新检查计划" },
  ]);
  assert.deepEqual(retryOptionsForFailure("template_fill_apply.command", "template-fill"), [
    { phase: "template_fill_apply", label: "重新填充 PPTX" },
  ]);
  assert.deepEqual(retryOptionsForFailure("template_fill_validate.contract", "template-fill"), [
    { phase: "template_fill_validate", label: "重新校验结果" },
  ]);
  assert.deepEqual(retryOptionsForFailure("publish.contract", "template-fill"), [
    { phase: "publish", label: "重新发布" },
  ]);
  assert.deepEqual(retryOptionsForFailure("template_resolve", "main"), [{ phase: "prepare", label: "重新准备" }]);
  assert.equal(retryOptionsForFailure("publish.contract", "main").length, 5);
  assert.match(retryGuidanceForFailure("template_fill_plan.inputs"), /多个.*PPTX/);
  assert.match(retryGuidanceForFailure("template_fill_plan.inputs"), /没有源文件删除 API/);
  assert.match(retryGuidanceForFailure("template_fill_plan.inputs"), /恰好一个.*\.pptx.*可读内容/);
  assert.equal(retryGuidanceForFailure("template_fill_plan.contract"), "");
});

test("completed navigation, plan entry, and artifact visibility are route-aware", async () => {
  const { canOpenTemplateFillPlan, completedTaskRoute, visibleTaskArtifacts } = await loadAppHelpersModule();
  assert.deepEqual(completedTaskRoute("task-1", "main"), { name: "preview", id: "task-1" });
  assert.deepEqual(completedTaskRoute("task-1", "template-fill"), { name: "templateFill", id: "task-1" });
  for (const status of [
    "awaiting_template_fill_confirm", "template_fill_checking", "template_fill_applying",
    "template_fill_validating", "completed", "failed",
  ]) {
    assert.equal(canOpenTemplateFillPlan({ route: "template-fill", status }), true, status);
  }
  assert.equal(canOpenTemplateFillPlan({ route: "main", status: "completed" }), false);
  const artifacts = Array.from({ length: 12 }, (_, index) => ({ id: String(index) }));
  assert.equal(visibleTaskArtifacts(artifacts, "main").length, 8);
  assert.equal(visibleTaskArtifacts(artifacts, "template-fill").length, 12);
});

test("Template Fill component uses production helpers and required actions", () => {
  const app = appFunctionSource("App");
  const page = appFunctionSource("TemplateFillPlanPage");
  const detail = appFunctionSource("TaskDetailPage");
  assert.match(app, /route\.name === "templateFill"\s*&&\s*<TemplateFillPlanPage taskId=\{route\.id\}/);
  for (const label of ["返回", "重新生成计划", "保存 JSON", "检查计划", "确认并导出", "打开填充计划"]) {
    assert.ok(appSource.includes(label), `missing Template Fill action ${label}`);
  }
  assert.match(page, /templateFillActionState\s*\(/);
  assert.match(page, /templateFillSlideRows\s*\(/);
  assert.match(page, /templateFillCheckRows\s*\(/);
  assert.match(page, /uploaded PPTX|上传的 PPTX/);
  assert.match(detail, /visibleTaskArtifacts\s*\(/);
  assert.match(detail, /canOpenTemplateFillPlan\s*\(/);
  assert.match(detail, /completedTaskRoute\s*\(/);
  assert.match(detail, /taskRoute !== "template-fill"[\s\S]*?<span>SVG<\/span>/);
});

test("save and check refetch canonical previews while only confirm advances", () => {
  const page = appFunctionSource("TemplateFillPlanPage");
  const save = page.match(/async function savePlan\(\)[\s\S]*?(?=async function checkPlan)/)?.[0] || "";
  const check = page.match(/async function checkPlan\(\)[\s\S]*?(?=async function confirmPlan)/)?.[0] || "";
  const confirm = page.match(/async function confirmPlan\(\)[\s\S]*?(?=\n\s*return \()/)?.[0] || "";
  assert.match(save, /api\.saveTemplateFillPlan/);
  assert.match(save, /adoptPreview\(saved\)[\s\S]*api\.getTemplateFillPlan/);
  assert.match(check, /api\.checkTemplateFillPlan/);
  assert.match(check, /can_confirm:\s*false/);
  assert.match(check, /api\.getTemplateFillPlan/);
  assert.doesNotMatch(check, /\bgo\s*\(/);
  assert.match(confirm, /api\.confirmTemplateFillPlan/);
  assert.match(confirm, /go\(\{ name: "task", id: taskId \}\)/);
});

test("task detail fetches Template Fill preview best-effort outside core loading", () => {
  const detail = appFunctionSource("TaskDetailPage");
  const core = detail.match(/Promise\.all\(\[([\s\S]*?)\]\)/)?.[1] || "";
  assert.ok(core, "missing task-detail core Promise.all");
  assert.doesNotMatch(core, /getTemplateFillPlan/);
  assert.match(detail, /if \(nextTask\.route === "template-fill"\)[\s\S]*?try[\s\S]*?api\.getTemplateFillPlan[\s\S]*?catch[\s\S]*?setTemplateFillPreview\(null\)/);
});

test("Template Fill layout is stable and collapses without narrow-screen overflow", () => {
  const editor = stylesSource.match(/\.plan-json-editor\s*\{([^}]*)\}/s);
  assert.ok(editor, "missing .plan-json-editor rule");
  assert.match(editor[1], /width:\s*100%\s*;/);
  assert.match(editor[1], /min-width:\s*0\s*;/);
  assert.match(editor[1], /min-height:\s*520px\s*;/);

  const baseRow = stylesSource.match(/\.check-report-row\s*\{([^}]*)\}/s);
  const badRow = stylesSource.match(/\.check-report-row\.bad\s*\{([^}]*)\}/s);
  const warnRow = stylesSource.match(/\.check-report-row\.warn\s*\{([^}]*)\}/s);
  assert.ok(baseRow && badRow && warnRow, "missing stable report row rules");
  assert.match(baseRow[1], /display:\s*grid\s*;/);
  assert.match(baseRow[1], /padding:/);
  assert.match(baseRow[1], /border(?:-bottom)?:/);
  assert.doesNotMatch(`${badRow[1]}${warnRow[1]}`, /(?:display|grid-template-columns|padding|margin|border-width):/);

  const tablet = mediaQuerySource(980);
  assert.match(tablet, /\.template-fill-layout[\s\S]*grid-template-columns:\s*1fr\s*;/);

  const narrow = mediaQuerySource(620);
  assert.match(narrow, /\.plan-slide-row[\s\S]*grid-template-columns:\s*1fr\s*;/);
  assert.match(narrow, /\.check-report-row[\s\S]*grid-template-columns:\s*1fr\s*;/);
  assert.match(narrow, /overflow-wrap:\s*anywhere\s*;/);
});

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
  const { retryOptionsForFailure } = await loadAppHelpersModule();
  assert.deepEqual(retryOptionsForFailure("template_resolve"), [
    { phase: "prepare", label: "重新准备" },
  ]);
});
