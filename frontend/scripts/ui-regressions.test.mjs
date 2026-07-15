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
    "templateFillPlanReadableStatuses",
    "beautifyPlanStatuses",
  ]);
  const functionNames = new Set([
    "isConfirmationStatus",
    "isWaitingStatus",
    "numberFromSummary",
    "emptyTaskResources",
    "emptySVGBundle",
    "resourceItemsByStatus",
    "fidelityMetricCount",
    "beautifyFidelityMetricText",
    "isFullSVGRouteUI",
    "templateFillText",
    "templateFillSlideRows",
    "templateFillCheckRows",
    "templateFillActionState",
    "beautifyPlanSlideRows",
    "beautifyPlanErrorCount",
    "beautifyPlanActionState",
    "templateFillBasename",
    "templateFillPageKey",
    "beautifyPlanPageKey",
    "taskDetailPageKey",
    "createTaskDetailRequestScope",
    "loadTaskDetailData",
    "taskDetailRetryTaskID",
    "templateFillPlanReadableStatus",
    "beautifyPlanReadableStatus",
    "createTemplateFillRequestScope",
    "templateFillScopedTaskID",
    "scopedTemplateFillRequest",
    "startTemplateFillRequestGeneration",
    "createBeautifyPlanRequestScope",
    "scopedBeautifyPlanRequest",
    "taskRouteMatches",
    "retryOptionsForFailure",
    "retryGuidanceForFailure",
    "canOpenTemplateFillPlan",
    "canOpenBeautifyPlan",
    "completedTaskRoute",
    "visibleTaskArtifacts",
    "loadPreviewPageData",
    "previewPageKey",
    "createPreviewPageState",
    "previewPageStateForTask",
    "confirmationSubmissionValues",
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

async function loadAPIHelpersModule() {
  const sourceFile = ts.createSourceFile("api.ts", apiSource, ts.ScriptTarget.ES2020, true, ts.ScriptKind.TS);
  let declaration = "";
  sourceFile.forEachChild((node) => {
    if (ts.isFunctionDeclaration(node) && node.name?.text === "normalizeTaskQuality") {
      declaration = node.getText(sourceFile);
    }
  });
  assert.ok(declaration, "missing normalizeTaskQuality declaration");
  return loadSourceModule(`${declaration}\nexport { normalizeTaskQuality };`, "api.ts");
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

test("Beautify API surface exposes typed plan and fidelity contracts", () => {
  const taskStatus = typeAliasSource(apiSource, "api.ts", "TaskStatus");
  const pipelinePhase = typeAliasSource(apiSource, "api.ts", "PipelinePhase");
  const retryPhase = typeAliasSource(apiSource, "api.ts", "RetryPhase");
  for (const status of ["beautify_inventory_building", "beautify_planning", "awaiting_beautify_confirm"]) {
    assert.match(taskStatus, new RegExp(`\\| \\"${status}\\"`));
  }
  for (const phase of ["beautify_inventory", "beautify_plan"]) {
    assert.match(pipelinePhase, new RegExp(`\\| \\"${phase}\\"`));
    assert.match(retryPhase, new RegExp(`\\| \\"${phase}\\"`));
  }
  const preview = typeAliasSource(apiSource, "api.ts", "BeautifyPlanPreview");
  for (const field of [
    "task_id", "source", "identity", "inventory", "risks", "plan", "findings",
    "summary", "plan_sha256", "revision", "can_edit", "can_confirm",
  ]) {
    assert.match(preview, new RegExp(`\\b${field}:`), `missing BeautifyPlanPreview.${field}`);
  }
  const slide = typeAliasSource(apiSource, "api.ts", "BeautifyPlanSlide");
  for (const field of [
    "source_slide", "output_page", "page_rhythm", "layout_strategy", "text_block_ids",
    "image_ids", "table_ids", "chart_ids", "ignored", "unsupported", "risks",
  ]) {
    assert.match(slide, new RegExp(`\\b${field}:`), `missing BeautifyPlanSlide.${field}`);
  }
  const source = typeAliasSource(apiSource, "api.ts", "BeautifySourceSummary");
  for (const forbidden of ["path", "project_path", "content", "source_binary", "host_path"]) {
    assert.doesNotMatch(source, new RegExp(`\\b${forbidden}\\??:`), `unsafe BeautifySourceSummary.${forbidden}`);
  }
  const fidelity = typeAliasSource(apiSource, "api.ts", "BeautifyFidelitySummary");
  for (const field of [
    "decision", "source_slide_count", "output_slide_count", "pages", "identity",
    "ignored", "unsupported", "warning", "error", "blocking", "report_artifact_id",
  ]) {
    assert.match(fidelity, new RegExp(`\\b${field}:`), `missing BeautifyFidelitySummary.${field}`);
  }
  for (const method of [
    "getBeautifyPlan", "saveBeautifyPlan", "checkBeautifyPlan", "confirmBeautifyPlan", "regenerateBeautifyPlan",
  ]) {
    assert.match(apiSource, new RegExp(`\\b${method}:`), `missing API method ${method}`);
  }
  assert.match(apiSource, /\/beautify-plan/);
  assert.match(apiSource, /\/beautify-plan\/check/);
  assert.match(apiSource, /\/beautify-plan\/confirm/);
  assert.match(apiSource, /\/beautify-plan\/regenerate/);
  assert.match(apiSource, /saveBeautifyPlan:[\s\S]*?method:\s*"PUT"[\s\S]*?expected_plan_sha256/);
});

test("resource API surface exposes only safe summary and artifact-bound preview fields", () => {
  const retryPhase = typeAliasSource(apiSource, "api.ts", "RetryPhase");
  const summary = typeAliasSource(apiSource, "api.ts", "ResourceSummary");
  const item = typeAliasSource(apiSource, "api.ts", "TaskResourceItem");
  const resources = typeAliasSource(apiSource, "api.ts", "TaskResources");
  assert.match(retryPhase, /\| "image_acquire"/);
  for (const field of ["total", "ready", "degraded", "failed", "pending", "required_failed", "bytes"]) {
    assert.match(summary, new RegExp(`\\b${field}:`));
  }
  for (const field of [
    "id", "page", "type", "purpose", "required", "acquire_via", "provider", "status",
    "fallback", "publishable", "artifact_id", "mime_type", "size", "width", "height", "error_code", "error",
  ]) {
    assert.match(item, new RegExp(`\\b${field}\\??:`), `missing TaskResourceItem.${field}`);
  }
  for (const forbidden of ["path", "prompt", "source_url", "credential"]) {
    assert.doesNotMatch(item, new RegExp(`\\b${forbidden}\\??:`), `unsafe TaskResourceItem.${forbidden}`);
  }
  for (const field of ["task_id", "phase_status", "summary", "resources", "manifest_sha256"]) {
    assert.match(resources, new RegExp(`\\b${field}:`));
  }
  assert.match(apiSource, /getResources:\s*\(id: string\)\s*=>\s*request<TaskResources>/);
  assert.match(apiSource, /\/tasks\/\$\{encodeURIComponent\(id\)\}\/resources/);
});

test("SVG bundle API and Executor summary stay task-scoped and expose no SVG XML", () => {
  const bundle = typeAliasSource(apiSource, "api.ts", "SVGBundleSummary");
  const page = typeAliasSource(apiSource, "api.ts", "SVGPageSummary");
  for (const field of [
    "task_id", "phase_status", "passed", "canvas", "page_count", "pages", "resource_summary",
    "chart_summary", "notes", "errors", "warnings", "artifact_ids", "inventory_sha256", "phase_run_id",
  ]) {
    assert.match(bundle, new RegExp(`\\b${field}:`), `missing SVGBundleSummary.${field}`);
  }
  for (const field of [
    "page_id", "page", "filename", "sha256", "text_count", "image_count", "chart_count",
    "resource_count", "notes_present", "warnings", "artifact_id",
  ]) {
    assert.match(page, new RegExp(`\\b${field}\\??:`), `missing SVGPageSummary.${field}`);
  }
  for (const forbidden of ["project_path", "svg_xml", "source_xml", "host_path"]) {
    assert.doesNotMatch(bundle, new RegExp(`\\b${forbidden}:`));
    assert.doesNotMatch(page, new RegExp(`\\b${forbidden}:`));
  }
  assert.match(apiSource, /getSVGBundle:\s*\(id: string\)\s*=>\s*request<SVGBundleSummary>/);
  assert.match(apiSource, /\/tasks\/\$\{encodeURIComponent\(id\)\}\/svg-bundle/);
  assert.match(appSource, /Executor \/ SVG 契约/);
  assert.match(appSource, /尚无已通过契约的 SVG bundle/);
});

test("quality API and completed warning view remain task-scoped", () => {
  const quality = typeAliasSource(apiSource, "api.ts", "TaskQuality");
  const finding = typeAliasSource(apiSource, "api.ts", "QualityFinding");
  for (const field of [
    "task_id", "current_gate", "decision", "warning_badge", "svg_summary", "pptx_summary",
    "findings", "chart_receipts", "text_coverage", "render_artifact_ids",
    "contact_sheet_artifact_id", "readback_artifact_id", "allowed_retry_phases", "beautify_fidelity",
  ]) {
    assert.match(quality, new RegExp(`\\b${field}\\??:`), `missing TaskQuality.${field}`);
  }
  for (const field of ["rule", "severity", "page_id", "message", "owner_phase", "retry_phase"]) {
    assert.match(finding, new RegExp(`\\b${field}:`), `missing QualityFinding.${field}`);
  }
  for (const forbidden of ["evidence", "host_path", "project_path", "svg_xml"]) {
    assert.doesNotMatch(finding, new RegExp(`\\b${forbidden}:`));
  }
  assert.match(apiSource, /getQuality:\s*\(id: string\)\s*=>\s*request<TaskQuality>/);
  assert.match(apiSource, /\/tasks\/\$\{encodeURIComponent\(id\)\}\/quality/);
  assert.match(apiSource, /request<TaskQuality>[\s\S]*?\.then\(normalizeTaskQuality\)/);
  assert.match(appSource, /生成质量门禁/);
  assert.match(appSource, /quality\.warning_badge/);
  assert.match(appSource, /联系表/);
  assert.match(appSource, /PPTX 回读/);
  assert.match(appSource, /源 PPTX → 输出 PPTX 保真/);
});

test("completed historical quality payloads normalize nullable collections", async () => {
  const { normalizeTaskQuality } = await loadAPIHelpersModule();
  const normalized = normalizeTaskQuality({
    task_id: "33509197-30b2-4ed5-9681-e1bdb03b74d7",
    decision: "pending",
    findings: null,
    chart_receipts: null,
    render_artifact_ids: null,
    allowed_retry_phases: null,
    beautify_fidelity: {
      pages: null,
      identity: null,
      ignored: null,
      unsupported: null,
    },
  });
  assert.deepEqual(normalized.findings, []);
  assert.deepEqual(normalized.chart_receipts, []);
  assert.deepEqual(normalized.render_artifact_ids, []);
  assert.deepEqual(normalized.allowed_retry_phases, []);
  assert.deepEqual(normalized.beautify_fidelity.pages, []);
  assert.deepEqual(normalized.beautify_fidelity.identity, {
    selected_source: "",
    overrides: [],
    font_substitutions: [],
  });
  assert.deepEqual(normalized.beautify_fidelity.ignored, []);
  assert.deepEqual(normalized.beautify_fidelity.unsupported, []);
});

test("Template Fill router parses and serializes the plan hash", async () => {
  globalThis.window = { location: { hash: "#/tasks/task%20one/template-fill" } };
  const { parseRoute, routeToHash } = await loadSourceModule(routerSource, "router.ts");
  assert.deepEqual(parseRoute(), { name: "templateFill", id: "task%20one" });
  assert.equal(routeToHash({ name: "templateFill", id: "task%20one" }), "#/tasks/task%20one/template-fill");
  window.location.hash = routeToHash({ name: "templateFill", id: "task-2" });
  assert.deepEqual(parseRoute(), { name: "templateFill", id: "task-2" });
});

test("Beautify router parses and serializes the typed plan hash", async () => {
  globalThis.window = { location: { hash: "#/tasks/task%20one/beautify-plan" } };
  const { parseRoute, routeToHash } = await loadSourceModule(routerSource, "router.ts");
  assert.deepEqual(parseRoute(), { name: "beautifyPlan", id: "task%20one" });
  assert.equal(routeToHash({ name: "beautifyPlan", id: "task-2" }), "#/tasks/task-2/beautify-plan");
});

test("replaceRoute canonicalizes with history replacement and dispatches routing", async () => {
  const calls = [];
  globalThis.window = {
    location: { hash: "#/tasks/task-1/preview" },
    history: {
      state: { preserved: true },
      replaceState: (state, title, url) => calls.push({ state, title, url }),
    },
    dispatchEvent: (event) => calls.push({ event: event.type }),
  };
  const { replaceRoute } = await loadSourceModule(routerSource, "router.ts");
  replaceRoute({ name: "templateFill", id: "task-1" });
  assert.deepEqual(calls, [
    { state: { preserved: true }, title: "", url: "#/tasks/task-1/template-fill" },
    { event: "hashchange" },
  ]);
  assert.equal(window.location.hash, "#/tasks/task-1/preview", "replaceRoute must not push by assigning location.hash");
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

test("Beautify labels, statuses, phases, and artifacts are route-aware", async () => {
  const [{ statusLabel, statusTone, phaseLabel, artifactKindLabel }, helpers] = await Promise.all([
    loadSourceModule(formatSource, "format.ts"),
    loadAppHelpersModule(),
  ]);
  assert.deepEqual(
    ["beautify_inventory_building", "beautify_planning", "awaiting_beautify_confirm"].map(
      (status) => [statusLabel[status], statusTone[status]],
    ),
    [["构建美化清单", "active"], ["生成美化计划", "active"], ["审查美化计划", "waiting"]],
  );
  assert.deepEqual(
    ["beautify_inventory", "beautify_plan"].map((phase) => phaseLabel[phase]),
    ["美化清单", "美化计划"],
  );
  for (const kind of [
    "beautify_inputs", "beautify_inventory", "beautify_risk_report", "beautify_plan",
    "beautify_lock", "beautify_fidelity_report", "source_svg_reference",
  ]) {
    assert.ok(artifactKindLabel[kind], `missing ${kind} label`);
  }
  assert.ok(helpers.activeStatuses.includes("beautify_inventory_building"));
  assert.ok(helpers.activeStatuses.includes("beautify_planning"));
  assert.equal(helpers.isWaitingStatus("awaiting_beautify_confirm"), true);
  assert.equal(helpers.isFullSVGRouteUI("main"), true);
  assert.equal(helpers.isFullSVGRouteUI("beautify"), true);
  assert.equal(helpers.isFullSVGRouteUI("template-fill"), false);
});

test("runner profiles expose locked engine labels and resource phase copy", async () => {
  const { phaseLabel, runnerProfileLabel, runnerProfileSourceLabel, statusLabel, taskRunnerProfileLabel } = await loadSourceModule(formatSource, "format.ts");
  assert.equal(runnerProfileLabel["full-ppt-master"], "Full PPT Master");
  assert.match(runnerProfileLabel["real-lite"], /测试\/降级/);
  assert.equal(runnerProfileLabel.smoke, "Smoke（测试 fixture）");
  assert.equal(taskRunnerProfileLabel("full-ppt-master", "template-fill"), "Native Template Fill");
  assert.equal(taskRunnerProfileLabel("", "main"), "未锁定");
  assert.equal(runnerProfileSourceLabel.deployment_default, "部署默认");
  for (const field of ["runner_profile", "runner_profile_source", "runner_profile_locked_at"]) {
    assert.match(apiSource, new RegExp(`\\b${field}`), `missing Task.${field}`);
  }
  assert.doesNotMatch(appSource, /资源阶段尚未启用（兼容跳过）/);
  assert.equal(statusLabel.image_acquiring, "正在准备图片、图标、公式与图表资源");
  assert.equal(phaseLabel.image_acquire, "资源准备");
  assert.match(appSource, /任务已进入运行阶段但引擎尚未锁定/);
});

test("resource grouping keeps ready, degraded, and blocking states separate", async () => {
  const { emptySVGBundle, emptyTaskResources, resourceItemsByStatus } = await loadAppHelpersModule();
  assert.equal(emptyTaskResources("task-resource").task_id, "task-resource");
  assert.deepEqual(emptyTaskResources().resources, []);
  assert.equal(emptySVGBundle("task-svg").task_id, "task-svg");
  assert.deepEqual(emptySVGBundle().pages, []);
  const grouped = resourceItemsByStatus([
    { id: "ready", status: "ready" },
    { id: "degraded", status: "degraded" },
    { id: "skipped", status: "skipped" },
    { id: "failed", status: "failed" },
    { id: "pending", status: "pending" },
  ]);
  assert.deepEqual(grouped.ready.map((item) => item.id), ["ready"]);
  assert.deepEqual(grouped.degraded.map((item) => item.id), ["degraded", "skipped"]);
  assert.deepEqual(grouped.failed.map((item) => item.id), ["failed", "pending"]);
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

test("Beautify typed rows expose only counts and editable visual controls", async () => {
  const { beautifyPlanSlideRows, beautifyPlanActionState, beautifyPlanErrorCount } = await loadAppHelpersModule();
  const preview = {
    inventory: { pages: [{ source_slide: 1, text_count: 4, image_count: 1, table_count: 0, chart_count: 1 }] },
    plan: {
      slides: [{
        source_slide: 1, output_page: 1, page_role: "cover", page_rhythm: "anchor",
        layout_strategy: "hero", text_block_ids: ["text-1"], image_ids: ["image-1"],
        table_ids: [], chart_ids: ["chart-1"], ignored: [], unsupported: [], risks: ["risk-1"],
      }],
    },
    findings: [{ severity: "warning" }, { severity: "error" }],
  };
  const rows = beautifyPlanSlideRows(preview);
  assert.equal(rows.length, 1);
  assert.equal(rows[0].source_slide, 1);
  assert.equal(rows[0].output_page, 1);
  assert.equal(rows[0].inventory.text_count, 4);
  assert.equal(beautifyPlanErrorCount(preview), 1);
  const base = {
    canEdit: true, canConfirm: true, taskStatus: "awaiting_beautify_confirm",
    busy: false, dirty: false, errorCount: 0,
  };
  assert.equal(beautifyPlanActionState(base).confirmDisabled, false);
  assert.equal(beautifyPlanActionState({ ...base, dirty: true }).confirmDisabled, true);
  assert.equal(beautifyPlanActionState({ ...base, errorCount: 1 }).confirmDisabled, true);
  assert.equal(beautifyPlanActionState({ ...base, dirty: false }).saveDisabled, true);
  assert.equal(beautifyPlanActionState({ ...base, dirty: true }).saveDisabled, false);
});

test("Beautify fidelity metric counts are safe for numeric and ID-array payloads", async () => {
  const { fidelityMetricCount, beautifyFidelityMetricText } = await loadAppHelpersModule();
  assert.equal(fidelityMetricCount(2), 2);
  assert.equal(fidelityMetricCount(["a", "b"]), 2);
  assert.equal(fidelityMetricCount(undefined), 0);
  assert.equal(beautifyFidelityMetricText({ expected: 5, matched: 5 }), "5/5");
  assert.equal(
    beautifyFidelityMetricText({ expected: 5, matched: 4, missing: ["text-3"], changed: 1 }),
    "4/5 · 2 异常",
  );
  assert.equal(beautifyFidelityMetricText({ expected: 0, matched: 0, required: 1, used: 1 }), "1/1");
});

test("Template Fill task switches fail closed and discard late task responses", async () => {
  const {
    createTemplateFillRequestScope,
    scopedTemplateFillRequest,
    startTemplateFillRequestGeneration,
    templateFillActionState,
    templateFillPageKey,
    templateFillScopedTaskID,
  } = await loadAppHelpersModule();

  assert.notEqual(templateFillPageKey("task-a"), templateFillPageKey("task-b"), "task IDs must remount the page");
  let currentHashTaskId = "task-a";
  const scopeA = createTemplateFillRequestScope("task-a", () => currentHashTaskId === "task-a");
  let resolveStrictProbe;
  let strictProbeRequest;
  const cleanupStrictProbe = startTemplateFillRequestGeneration(scopeA, () => {
    strictProbeRequest = scopedTemplateFillRequest(scopeA, "task-a", () => new Promise((resolve) => {
      resolveStrictProbe = resolve;
    }));
  });
  cleanupStrictProbe();
  let cleanupLiveGeneration = startTemplateFillRequestGeneration(scopeA, () => {});
  assert.deepEqual(
    await scopedTemplateFillRequest(scopeA, "task-a", async (id) => ({ task_id: id, generation: "live" })),
    { task_id: "task-a", generation: "live" },
    "StrictMode's second effect setup must receive a live generation",
  );
  resolveStrictProbe({ task_id: "task-a", generation: "stale-probe" });
  assert.equal(await strictProbeRequest, undefined, "the first StrictMode setup cannot overwrite the live setup");

  let rejectStaleProbe;
  const staleProbeFailure = scopedTemplateFillRequest(scopeA, "task-a", () => new Promise((_, reject) => {
    rejectStaleProbe = reject;
  }));
  cleanupLiveGeneration();
  cleanupLiveGeneration = startTemplateFillRequestGeneration(scopeA, () => {});
  rejectStaleProbe(new Error("stale StrictMode request"));
  assert.equal(await staleProbeFailure, undefined, "a stale StrictMode rejection cannot poison the live setup");
  assert.equal(templateFillScopedTaskID(scopeA, "task-a"), "task-a");

  currentHashTaskId = "task-b";
  assert.equal(templateFillScopedTaskID(scopeA, "task-b"), "", "task A JSON cannot target task B");

  let mismatchedRequestCalled = false;
  assert.equal(
    await scopedTemplateFillRequest(scopeA, "task-b", async () => {
      mismatchedRequestCalled = true;
      return "wrong";
    }),
    undefined,
  );
  assert.equal(mismatchedRequestCalled, false);

  currentHashTaskId = "task-a";
  let resolveLate;
  const pendingA = scopedTemplateFillRequest(scopeA, "task-a", () => new Promise((resolve) => {
    resolveLate = resolve;
  }));
  currentHashTaskId = "task-b";
  resolveLate({ task_id: "task-a", plan: { title: "A" } });
  assert.equal(await pendingA, undefined, "a hash change invalidates A before React effect cleanup");
  cleanupLiveGeneration();

  const scopeB = createTemplateFillRequestScope("task-b", () => currentHashTaskId === "task-b");
  scopeB.activate();
  assert.deepEqual(
    await scopedTemplateFillRequest(scopeB, "task-b", async (id) => ({ task_id: id, plan: { title: "B" } })),
    { task_id: "task-b", plan: { title: "B" } },
  );
  assert.deepEqual(
    templateFillActionState({
      canEdit: false,
      canConfirm: false,
      taskStatus: undefined,
      busy: false,
      dirty: false,
      checkErrorCount: 0,
    }),
    { saveDisabled: true, checkDisabled: true, confirmDisabled: true, hint: "" },
    "the freshly keyed task B page starts with actions closed",
  );
});

test("task detail discards delayed A and older overlapping poll snapshots", async () => {
  const {
    createTaskDetailRequestScope,
    loadTaskDetailData,
    taskDetailPageKey,
    taskDetailRetryTaskID,
    templateFillPlanReadableStatus,
    beautifyPlanReadableStatus,
  } = await loadAppHelpersModule();

  assert.notEqual(taskDetailPageKey("task-a"), taskDetailPageKey("task-b"), "task IDs must remount detail state");
  assert.equal(templateFillPlanReadableStatus({ route: "template-fill", status: "awaiting_template_fill_confirm" }), true);
  assert.equal(templateFillPlanReadableStatus({ route: "template-fill", status: "publishing" }), true);
  assert.equal(templateFillPlanReadableStatus({ route: "template-fill", status: "template_fill_planning" }), false);
  assert.equal(templateFillPlanReadableStatus({ route: "template-fill", status: "source_converting" }), false);
  assert.equal(templateFillPlanReadableStatus({ route: "template-fill", status: "cancelled" }), false);
  assert.equal(templateFillPlanReadableStatus({ route: "main", status: "completed" }), false);
  assert.equal(beautifyPlanReadableStatus({ route: "beautify", status: "awaiting_beautify_confirm" }), true);
  assert.equal(beautifyPlanReadableStatus({ route: "beautify", status: "beautify_planning" }), false);
  assert.equal(beautifyPlanReadableStatus({ route: "main", status: "completed" }), false);

  const deferred = () => {
    let resolve;
    let reject;
    const promise = new Promise((resolvePromise, rejectPromise) => {
      resolve = resolvePromise;
      reject = rejectPromise;
    });
    return { promise, resolve, reject };
  };
  const requestSet = (task, waits = {}) => ({
    getTask: () => waits.task?.promise || Promise.resolve(task),
    listEvents: () => waits.events?.promise || Promise.resolve([{ task_id: task.id, kind: "event" }]),
    listArtifacts: () => waits.artifacts?.promise || Promise.resolve([{ task_id: task.id, kind: "artifact" }]),
    getResources: () => waits.resources?.promise || Promise.resolve({ task_id: task.id, summary: { total: 1 }, resources: [{ id: `resource-${task.id}` }] }),
    getSVGBundle: () => waits.svgBundle?.promise || Promise.resolve({ task_id: task.id, passed: true, pages: [{ page_id: `page-${task.id}` }] }),
    getQuality: () => waits.quality?.promise || Promise.resolve({ task_id: task.id, decision: "pass", findings: [] }),
    listRuntimeRuns: () => waits.runtimeRuns?.promise || Promise.resolve([{ task_id: task.id, kind: "runtime" }]),
    listPhaseRuns: () => waits.phaseRuns?.promise || Promise.resolve([{ task_id: task.id, kind: "phase" }]),
    getTemplateFillPlan: () => waits.preview?.promise || Promise.resolve({ task_id: task.id, plan: { title: task.id } }),
    getBeautifyPlan: () => waits.beautifyPreview?.promise || Promise.resolve({ task_id: task.id, plan: { title: task.id } }),
  });

  let currentTaskId = "task-a";
  const delayedATask = deferred();
  const scopeA = createTaskDetailRequestScope("task-a", () => currentTaskId === "task-a");
  const pendingA = loadTaskDetailData(
    scopeA,
    "task-a",
    requestSet({ id: "task-a", route: "template-fill", status: "completed" }, { task: delayedATask }),
  );

  currentTaskId = "task-b";
  const scopeB = createTaskDetailRequestScope("task-b", () => currentTaskId === "task-b");
  const snapshotB = await loadTaskDetailData(
    scopeB,
    "task-b",
    requestSet({ id: "task-b", route: "template-fill", status: "completed" }),
  );
  assert.equal(snapshotB.task.id, "task-b");
  assert.equal(snapshotB.events[0].task_id, "task-b");
  assert.equal(snapshotB.artifacts[0].task_id, "task-b");
  assert.equal(snapshotB.resources.task_id, "task-b");
  assert.equal(snapshotB.resources.resources[0].id, "resource-task-b");
  assert.equal(snapshotB.svgBundle.task_id, "task-b");
  assert.equal(snapshotB.svgBundle.pages[0].page_id, "page-task-b");
  assert.equal(snapshotB.quality.task_id, "task-b");
  assert.equal(snapshotB.runtimeRuns[0].task_id, "task-b");
  assert.equal(snapshotB.phaseRuns[0].task_id, "task-b");
  assert.equal(snapshotB.templateFillPreview.task_id, "task-b");
  delayedATask.resolve({ id: "task-a", route: "template-fill", status: "completed" });
  assert.equal(await pendingA, undefined, "late A must not produce a commit-ready snapshot at the B URL");
  assert.equal(taskDetailRetryTaskID(scopeA, "task-a", "task-a"), "", "retry must not call A from the B URL");
  assert.equal(taskDetailRetryTaskID(scopeB, "task-b", "task-b"), "task-b");

  const oldTask = deferred();
  const oldEvents = deferred();
  currentTaskId = "task-b";
  const oldPoll = loadTaskDetailData(
    scopeB,
    "task-b",
    requestSet({ id: "task-b", route: "template-fill", status: "completed" }, { task: oldTask, events: oldEvents }),
  );
  const newPoll = await loadTaskDetailData(
    scopeB,
    "task-b",
    requestSet({ id: "task-b", route: "template-fill", status: "completed" }),
  );
  assert.equal(newPoll.task.id, "task-b");
  oldTask.resolve({ id: "task-b", route: "template-fill", status: "completed" });
  oldEvents.resolve([{ task_id: "task-b", generation: "old" }]);
  assert.equal(await oldPoll, undefined, "an older overlapping poll must not regress the latest snapshot");

  const strictTask = deferred();
  const strictProbe = loadTaskDetailData(
    scopeB,
    "task-b",
    requestSet({ id: "task-b", route: "template-fill", status: "completed" }, { task: strictTask }),
  );
  scopeB.deactivate();
  const strictLive = await loadTaskDetailData(
    scopeB,
    "task-b",
    requestSet({ id: "task-b", route: "template-fill", status: "completed" }),
  );
  strictTask.resolve({ id: "task-b", route: "template-fill", status: "completed" });
  assert.equal(await strictProbe, undefined, "StrictMode cleanup must invalidate the first setup");
  assert.equal(strictLive.task.id, "task-b", "StrictMode's second setup must remain usable");

  const mismatchedResources = await loadTaskDetailData(
    scopeB,
    "task-b",
    requestSet({ id: "task-b", route: "main", status: "completed" }, {
      resources: { promise: Promise.resolve({ task_id: "task-a", summary: { total: 9 }, resources: [{ id: "leak" }] }) },
    }),
  );
  assert.equal(mismatchedResources, undefined, "a resource response for another task must never be committed");

  const mismatchedBundle = await loadTaskDetailData(
    scopeB,
    "task-b",
    requestSet({ id: "task-b", route: "main", status: "completed" }, {
      svgBundle: { promise: Promise.resolve({ task_id: "task-a", passed: true, pages: [{ page_id: "leak" }] }) },
    }),
  );
  assert.equal(mismatchedBundle, undefined, "an SVG bundle response for another task must never be committed");

  const mismatchedQuality = await loadTaskDetailData(
    scopeB,
    "task-b",
    requestSet({ id: "task-b", route: "beautify", status: "completed" }, {
      quality: { promise: Promise.resolve({ task_id: "task-a", decision: "pass", beautify_fidelity: { pages: [] } }) },
    }),
  );
  assert.equal(mismatchedQuality, undefined, "Beautify fidelity from another task must never be committed");

  const mismatchedBeautifyPlan = await loadTaskDetailData(
    scopeB,
    "task-b",
    requestSet({ id: "task-b", route: "beautify", status: "completed" }, {
      beautifyPreview: { promise: Promise.resolve({ task_id: "task-a", plan: { title: "leak" } }) },
    }),
  );
  assert.equal(mismatchedBeautifyPlan, undefined, "a Beautify plan for another task must never be committed");
});

test("task detail preview requests are gated to backend-readable statuses", async () => {
  const { createTaskDetailRequestScope, loadTaskDetailData } = await loadAppHelpersModule();
  for (const status of ["template_fill_planning", "source_converting", "cancelled"]) {
    let previewCalls = 0;
    const scope = createTaskDetailRequestScope(`task-${status}`, () => true);
    const task = { id: `task-${status}`, route: "template-fill", status };
    const snapshot = await loadTaskDetailData(scope, task.id, {
      getTask: async () => task,
      listEvents: async () => [],
      listArtifacts: async () => [],
      getResources: async () => ({ task_id: task.id, summary: { total: 0 }, resources: [] }),
      getSVGBundle: async () => ({ task_id: task.id, passed: false, pages: [] }),
      getQuality: async () => ({ task_id: task.id, decision: "pending", findings: [] }),
      listRuntimeRuns: async () => [],
      listPhaseRuns: async () => [],
      getTemplateFillPlan: async () => {
        previewCalls += 1;
        return { task_id: task.id };
      },
    });
    assert.equal(previewCalls, 0, `${status} must not request the unreadable plan endpoint`);
    assert.equal(snapshot.templateFillPreview, null);
  }
});

test("Template Fill basename fallback tolerates malformed runtime input", async () => {
  const { templateFillBasename } = await loadAppHelpersModule();
  assert.equal(templateFillBasename("/workspace/projects/demo/sources/company.pptx"), "company.pptx");
  assert.equal(templateFillBasename("C:\\workspace\\sources\\brand.pptx"), "brand.pptx");
  assert.equal(templateFillBasename("  "), "-");
  assert.equal(templateFillBasename(null), "-");
  assert.equal(templateFillBasename({ path: "unsafe.pptx" }), "-");
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
  assert.equal(retryOptionsForFailure("publish.contract", "main").length, 7);
  assert.ok(retryOptionsForFailure("image_acquire.contract", "main").some((option) => option.phase === "image_acquire"));
  assert.match(retryGuidanceForFailure("template_fill_plan.inputs"), /多个.*PPTX/);
  assert.match(retryGuidanceForFailure("template_fill_plan.inputs"), /没有源文件删除 API/);
  assert.match(retryGuidanceForFailure("template_fill_plan.inputs"), /恰好一个.*\.pptx.*可读内容/);
  assert.equal(retryGuidanceForFailure("template_fill_plan.contract"), "");
  assert.deepEqual(retryOptionsForFailure("beautify_inventory.inputs", "beautify"), [
    { phase: "prepare", label: "重新准备" },
    { phase: "beautify_inventory", label: "重建美化清单" },
  ]);
  assert.deepEqual(retryOptionsForFailure("beautify_inventory.contract", "beautify"), [
    { phase: "beautify_inventory", label: "重建美化清单" },
  ]);
  assert.deepEqual(retryOptionsForFailure("beautify_plan.contract", "beautify"), [
    { phase: "beautify_plan", label: "重建美化计划" },
  ]);
  assert.ok(retryOptionsForFailure("pptx_validate.beautify_fidelity", "beautify").some((option) => option.phase === "pptx_validate"));
});

test("completed navigation, plan entry, and artifact visibility are route-aware", async () => {
  const { canOpenTemplateFillPlan, canOpenBeautifyPlan, completedTaskRoute, visibleTaskArtifacts } = await loadAppHelpersModule();
  assert.deepEqual(completedTaskRoute("task-1", "main"), { name: "preview", id: "task-1" });
  assert.deepEqual(completedTaskRoute("task-1", "template-fill"), { name: "templateFill", id: "task-1" });
  for (const status of [
    "awaiting_template_fill_confirm", "template_fill_checking", "template_fill_applying",
    "template_fill_validating", "completed", "failed",
  ]) {
    assert.equal(canOpenTemplateFillPlan({ route: "template-fill", status }), true, status);
  }
  assert.equal(canOpenTemplateFillPlan({ route: "main", status: "completed" }), false);
  for (const status of ["awaiting_beautify_confirm", "spec_generating", "pptx_validating", "completed", "failed"]) {
    assert.equal(canOpenBeautifyPlan({ route: "beautify", status }), true, status);
  }
  assert.equal(canOpenBeautifyPlan({ route: "main", status: "completed" }), false);
  const artifacts = Array.from({ length: 12 }, (_, index) => ({ id: String(index) }));
  assert.equal(visibleTaskArtifacts(artifacts, "main").length, 8);
  assert.equal(visibleTaskArtifacts(artifacts, "template-fill").length, 12);
  assert.equal(visibleTaskArtifacts(artifacts, "beautify").length, 12);
});

test("direct preview canonicalization fetches task first, skips Template Fill artifacts, and ignores stale completion", async () => {
  const { loadPreviewPageData, taskRouteMatches } = await loadAppHelpersModule();

  const templateCalls = [];
  const replacements = [];
  const templateResult = await loadPreviewPageData(
    "task-template",
    async (id) => {
      templateCalls.push(`task:${id}`);
      return { id, route: "template-fill" };
    },
    async (id) => {
      templateCalls.push(`artifacts:${id}`);
      throw new Error("Template Fill artifacts must not be fetched before redirect");
    },
    () => true,
    (route) => replacements.push(route),
  );
  assert.equal(templateResult, null);
  assert.deepEqual(templateCalls, ["task:task-template"]);
  assert.deepEqual(replacements, [{ name: "templateFill", id: "task-template" }]);

  const mainCalls = [];
  const mainResult = await loadPreviewPageData(
    "task-main",
    async (id) => {
      mainCalls.push(`task:${id}`);
      return { id, route: "main" };
    },
    async (id) => {
      mainCalls.push(`artifacts:${id}`);
      return [{ id: "svg-1", kind: "svg_final" }];
    },
    () => true,
    () => assert.fail("main preview must not redirect"),
  );
  assert.deepEqual(mainCalls, ["task:task-main", "artifacts:task-main"]);
  assert.deepEqual(mainResult, {
    task: { id: "task-main", route: "main" },
    artifacts: [{ id: "svg-1", kind: "svg_final" }],
  });

  let previewRoute = { name: "preview", id: "task-stale" };
  let resolveTask;
  const staleReplacements = [];
  const staleLoad = loadPreviewPageData(
    "task-stale",
    () => new Promise((resolve) => {
      resolveTask = resolve;
    }),
    async () => assert.fail("stale load must stop before artifacts"),
    () => taskRouteMatches(previewRoute, "preview", "task-stale"),
    (route) => staleReplacements.push(route),
  );
  previewRoute = { name: "tasks" };
  resolveTask({ id: "task-stale", route: "template-fill" });
  assert.equal(await staleLoad, null);
  assert.deepEqual(staleReplacements, [], "an unmounted preview cannot hijack later navigation");
});

test("preview task switches reset main A state before Template Fill B resolves or fails", async () => {
  const {
    createPreviewPageState,
    loadPreviewPageData,
    previewPageKey,
    previewPageStateForTask,
    taskRouteMatches,
  } = await loadAppHelpersModule();

  const loadedA = {
    ...createPreviewPageState("task-a"),
    task: { id: "task-a", title: "Main A", route: "main" },
    artifacts: [
      { id: "svg-a", kind: "svg_final", name: "A.svg" },
      { id: "pptx-a", kind: "pptx", name: "A.pptx" },
    ],
    selectedId: "svg-a",
  };
  assert.notEqual(previewPageKey("task-a"), previewPageKey("task-b"));
  assert.deepEqual(
    previewPageStateForTask(loadedA, "task-b"),
    createPreviewPageState("task-b"),
    "B URL must synchronously hide A metadata, artifacts, and selection",
  );

  let rejectTaskB;
  const routeB = { name: "preview", id: "task-b" };
  const pendingB = loadPreviewPageData(
    "task-b",
    () => new Promise((_, reject) => {
      rejectTaskB = reject;
    }),
    async () => assert.fail("failed B task fetch must not request artifacts"),
    () => taskRouteMatches(routeB, "preview", "task-b"),
    () => assert.fail("failed B task fetch must not redirect"),
  );
  const pendingState = previewPageStateForTask(loadedA, "task-b");
  assert.equal(pendingState.task, null);
  assert.deepEqual(pendingState.artifacts, []);
  assert.equal(pendingState.selectedId, "");
  rejectTaskB(new Error("task B unavailable"));
  await assert.rejects(pendingB, /task B unavailable/);
  const failedB = { ...createPreviewPageState("task-b"), error: "task B unavailable" };
  assert.equal(failedB.task, null);
  assert.deepEqual(failedB.artifacts, []);
  assert.equal(failedB.selectedId, "");
});

test("Template Fill component uses production helpers and required actions", () => {
  const app = appFunctionSource("App");
  const page = appFunctionSource("TemplateFillPlanPage");
  const detail = appFunctionSource("TaskDetailPage");
  const previewPage = appFunctionSource("PreviewPage");
  assert.match(app, /route\.name === "templateFill"[\s\S]*?<TemplateFillPlanPage key=\{templateFillPageKey\(route\.id\)\} taskId=\{route\.id\}/);
  assert.match(app, /route\.name === "task"[\s\S]*?<TaskDetailPage key=\{taskDetailPageKey\(route\.id\)\} taskId=\{route\.id\}/);
  for (const label of ["返回", "重新生成计划", "保存 JSON", "检查计划", "确认并导出", "打开填充计划"]) {
    assert.ok(appSource.includes(label), `missing Template Fill action ${label}`);
  }
  assert.match(page, /templateFillActionState\s*\(/);
  assert.match(page, /templateFillSlideRows\s*\(/);
  assert.match(page, /templateFillCheckRows\s*\(/);
  assert.match(page, /useState\(\(\) => createTemplateFillRequestScope\([\s\S]*?taskRouteMatches\(parseRoute\(\), "templateFill", taskId\)/);
  assert.match(page, /startTemplateFillRequestGeneration\(requestScope, \(generation\) => void load\(generation\)\)/);
  assert.match(page, /requestScope\.isGenerationCurrent\(generation, taskId\)/);
  assert.ok((page.match(/scopedTemplateFillRequest\s*\(/g) || []).length >= 7, "all plan requests must use the task scope");
  assert.doesNotMatch(page, /api\.(?:save|check|confirm|regenerate)TemplateFillPlan\(taskId/);
  assert.match(page, /uploaded PPTX|上传的 PPTX/);
  assert.match(detail, /visibleTaskArtifacts\s*\(/);
  assert.match(detail, /canOpenTemplateFillPlan\s*\(/);
  assert.match(detail, /completedTaskRoute\s*\(/);
  assert.match(detail, /createTaskDetailRequestScope\([\s\S]*?taskRouteMatches\(parseRoute\(\), "task", taskId\)/);
  assert.match(detail, /loadTaskDetailData\s*\(/);
  assert.match(detail, /getResources:\s*api\.getResources/);
  assert.match(detail, /resourceItemsByStatus\s*\(/);
  assert.match(detail, /api\.artifactContentUrl\(task\.id, item\.artifact_id\)/);
  assert.match(detail, /retry\("image_acquire"\)/);
  assert.match(detail, /taskDetailRetryTaskID\s*\(/);
  assert.match(detail, /const fullSVGRoute = isFullSVGRouteUI\(taskRoute\)/);
  assert.ok((detail.match(/\{fullSVGRoute && \(/g) || []).length >= 3, "resources, SVG, and quality must share the full-route guard");
  assert.match(previewPage, /loadPreviewPageData\([\s\S]*?replaceRoute/);
  assert.match(app, /route\.name === "preview"[\s\S]*?<PreviewPage key=\{previewPageKey\(route\.id\)\} taskId=\{route\.id\}/);
  assert.match(previewPage, /previewPageStateForTask\(state, taskId\)/);
  assert.match(previewPage, /catch \(err\)[\s\S]*?setState\(\{[\s\S]*?createPreviewPageState\(taskId\)/);
  assert.match(previewPage, /taskRouteMatches\(parseRoute\(\), "preview", taskId\)/);
  assert.match(previewPage, /catch \(err\)[\s\S]*?active && taskRouteMatches\(parseRoute\(\), "preview", taskId\)/);
  assert.match(previewPage, /return \(\) => \{\s*active = false;/);
});

test("Beautify component is task-scoped and never exposes a raw plan editor", () => {
  const app = appFunctionSource("App");
  const page = appFunctionSource("BeautifyPlanPage");
  const detail = appFunctionSource("TaskDetailPage");
  assert.match(app, /route\.name === "beautifyPlan"[\s\S]*?<BeautifyPlanPage key=\{beautifyPlanPageKey\(route\.id\)\} taskId=\{route\.id\}/);
  assert.match(page, /createBeautifyPlanRequestScope\([\s\S]*?taskRouteMatches\(parseRoute\(\), "beautifyPlan", taskId\)/);
  assert.ok((page.match(/scopedBeautifyPlanRequest\s*\(/g) || []).length >= 8, "all Beautify requests must be task scoped");
  assert.match(page, /if \(nextTask\.route !== "beautify"\)[\s\S]*?replaceRoute\(\{ name: "task", id: nextTask\.id \}\)/);
  assert.match(page, /nextPreview\.task_id !== taskId/);
  assert.doesNotMatch(page, /<textarea|plan-json-editor|保存 JSON/);
  assert.doesNotMatch(page, /onChange=\{[^}]*?(?:text_block_ids|table_ids|chart_ids|image_ids)/);
  for (const label of ["内容与数据已冻结", "页数、页面顺序、可见文字", "布局策略", "页面节奏", "风险决策", "确认美化计划"]) {
    assert.ok(page.includes(label), `missing Beautify UX copy: ${label}`);
  }
  assert.match(detail, /canOpenBeautifyPlan\s*\(/);
  assert.match(detail, /quality\.beautify_fidelity/);
  assert.match(detail, /源 PPTX → 输出 PPTX 保真/);
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

test("task detail commits one generation-scoped snapshot", () => {
  const detail = appFunctionSource("TaskDetailPage");
  assert.match(detail, /const next = await loadTaskDetailData\(/);
  assert.match(detail, /if \(next\)[\s\S]*?setDetail\(next\)/);
  assert.doesNotMatch(detail, /setTask\(|setEvents\(|setArtifacts\(|setRuntimeRuns\(|setPhaseRuns\(|setTemplateFillPreview\(|setBeautifyPlanPreview\(/);
  assert.match(detail, /return \(\) => \{[\s\S]*?requestScope\.deactivate\(\)/);
});

test("Template Fill deep link cannot regenerate a non-Template-Fill task", () => {
  const page = appFunctionSource("TemplateFillPlanPage");
  assert.match(page, /if \(nextTask\.route !== "template-fill"\)[\s\S]*?replaceRoute\(\{ name: "task", id: nextTask\.id \}\)/);
  assert.match(page, /const canRegenerate = task\?\.route === "template-fill"[\s\S]*?task\?\.status === "failed"/);
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

test("Beautify typed plan and fidelity layouts collapse without exposing frozen data editors", () => {
  const planLayout = stylesSource.match(/\.beautify-plan-layout\s*\{([^}]*)\}/s);
  const controls = stylesSource.match(/\.beautify-plan-controls\s*\{([^}]*)\}/s);
  const fidelity = stylesSource.match(/\.beautify-fidelity-row\s*\{([^}]*)\}/s);
  assert.ok(planLayout && controls && fidelity, "missing Beautify plan/fidelity layout rules");
  assert.match(planLayout[1], /grid-template-columns:/);
  assert.match(controls[1], /min-width:\s*0\s*;/);
  assert.match(fidelity[1], /grid-template-columns:/);
  const tablet = mediaQuerySource(980);
  assert.match(tablet, /\.beautify-plan-layout[\s\S]*grid-template-columns:\s*1fr\s*;/);
  const narrow = mediaQuerySource(620);
  assert.match(narrow, /\.beautify-fidelity-row[\s\S]*grid-template-columns:\s*1fr\s*;/);
  assert.match(narrow, /\.beautify-plan-controls[\s\S]*grid-template-columns:\s*1fr\s*;/);
  const page = appFunctionSource("BeautifyPlanPage");
  assert.doesNotMatch(page, /textarea|contenteditable/i);
});

test("Beautify Tier 2 keeps source page count read-only and out of the confirmation payload", async () => {
  const helpers = await loadAppHelpersModule();
  assert.deepEqual(
    helpers.confirmationSubmissionValues(
      { route: "beautify", status: "awaiting_realization_confirm" },
      { page_count: "4", slide_count: 4, color: "source", typography: "source fonts" },
    ),
    { color: "source", typography: "source fonts" },
  );
  assert.deepEqual(
    helpers.confirmationSubmissionValues(
      { route: "main", status: "awaiting_realization_confirm" },
      { page_count: "4", color: "source" },
    ),
    { page_count: "4", color: "source" },
  );
  assert.match(appSource, /confirmationSubmissionValues\(task, values\)/);
  assert.match(appSource, /locked=\{task\?\.route === "beautify" && confirmation\.key === "page_count"\}/);
  assert.match(appSource, /<input value=\{stringValue\} readOnly aria-readonly="true" \/>/);
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
