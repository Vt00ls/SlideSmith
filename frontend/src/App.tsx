import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Activity,
  ArrowLeft,
  CheckCircle2,
  Clock3,
  Download,
  Eye,
  FileText,
  FileUp,
  Image as ImageIcon,
  LayoutList,
  Layers,
  ListChecks,
  Loader2,
  Palette,
  Play,
  Plus,
  Presentation,
  RefreshCw,
  Search,
  TerminalSquare,
  Upload,
  XCircle,
} from "lucide-react";
import {
	api,
	APIError,
	Artifact,
	ArtifactVersion,
  BeautifyFidelityMetric,
  BeautifyPlan,
  BeautifyPlanPreview,
	Confirmation,
	EditRun,
	EditSession,
	ManualEditDraft,
	ManualEditOperation,
	ManualEditPageSnapshot,
	ManualEditSnapshotElement,
  parseJSON,
  RetryPhase,
  RuntimeRun,
  SpecPreview,
  SVGBundleSummary,
  Task,
  TaskEvent,
  TaskPhaseRun,
  TaskQuality,
  TaskResourceItem,
  TaskResources,
  TaskStatus,
  TemplateCatalogItem,
  TemplateFillPlanPreview,
} from "./api";
import {
  artifactKindLabel,
  formatBytes,
  formatTime,
  phaseLabel,
  routeLabel,
  runnerProfileSourceLabel,
  statusLabel,
  statusTone,
  taskRunnerProfileLabel,
} from "./format";
import { go, parseRoute, replaceRoute, Route } from "./router";

const activeStatuses: TaskStatus[] = [
  "runtime_preparing",
  "source_converting",
  "realization_deriving",
  "spec_generating",
  "template_fill_planning",
  "template_fill_checking",
  "template_fill_applying",
  "template_fill_validating",
  "beautify_inventory_building",
  "beautify_planning",
  "image_acquiring",
  "svg_generating",
  "quality_checking",
  "exporting",
  "pptx_validating",
  "publishing",
];

const confirmationStatuses: TaskStatus[] = ["awaiting_confirm", "awaiting_anchor_confirm", "awaiting_realization_confirm"];

function isConfirmationStatus(status?: TaskStatus) {
  return !!status && confirmationStatuses.includes(status);
}

function isWaitingStatus(status?: TaskStatus) {
  return isConfirmationStatus(status)
    || status === "awaiting_spec_confirm"
    || status === "awaiting_template_fill_confirm"
    || status === "awaiting_beautify_confirm";
}

const splitRetryOptions: Array<{ phase: RetryPhase; label: string }> = [
  { phase: "spec_generate", label: "重试规格" },
  { phase: "image_acquire", label: "重试资源准备" },
  { phase: "svg_execute", label: "重试 SVG" },
  { phase: "quality_check", label: "重跑质检" },
  { phase: "finalize_export", label: "重新导出" },
  { phase: "pptx_validate", label: "重新校验 PPTX" },
  { phase: "publish", label: "重新发布" },
];

const templateFillPlanInputRecoveryNote = "如果上传了多个 PPTX，SPEC3 没有源文件删除 API，重试无法修正输入集合；请新建一个修正后的任务，仅包含恰好一个上传的 .pptx 和至少一份可读内容。";

const templateFillPlanStatuses: TaskStatus[] = [
  "awaiting_template_fill_confirm",
  "template_fill_checking",
  "template_fill_applying",
  "template_fill_validating",
  "completed",
  "failed",
];

const templateFillPlanReadableStatuses: TaskStatus[] = [
  "awaiting_template_fill_confirm",
  "template_fill_checking",
  "template_fill_applying",
  "template_fill_validating",
  "publishing",
  "completed",
  "failed",
];

const beautifyPlanStatuses: TaskStatus[] = [
  "awaiting_beautify_confirm",
  "spec_generating",
  "awaiting_spec_confirm",
  "image_acquiring",
  "svg_generating",
  "quality_checking",
  "exporting",
  "pptx_validating",
  "publishing",
  "completed",
  "failed",
];

const templateSelectionKey = "slidesmith.newTask.templateId";
const supportedSourceAccept = [
  ".md", ".markdown", ".txt", ".text", ".csv", ".tsv", ".pdf",
  ".docx", ".doc", ".odt", ".rtf", ".epub", ".html", ".htm",
  ".tex", ".latex", ".rst", ".org", ".ipynb", ".typ",
  ".xlsx", ".xlsm", ".xls",
  ".pptx", ".pptm", ".ppsx", ".ppsm", ".potx", ".potm",
].join(",");
const templateKindFilters = [
  { value: "all", label: "全部" },
  { value: "layout", label: "版式" },
  { value: "deck", label: "成套" },
  { value: "brand", label: "品牌" },
];

function numberFromSummary(value: unknown) {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function emptyTaskResources(taskId = ""): TaskResources {
  return {
    task_id: taskId,
    phase_status: "",
    summary: { total: 0, ready: 0, degraded: 0, failed: 0, pending: 0, required_failed: 0, bytes: 0 },
    resources: [],
    manifest_sha256: "",
  };
}

function emptySVGBundle(taskId = ""): SVGBundleSummary {
  return {
    task_id: taskId,
    phase_status: "",
    passed: false,
    canvas: { id: "", width: 0, height: 0 },
    page_count: 0,
    pages: [],
    resource_summary: {},
    chart_summary: {},
    notes: { present: false, page_count: 0, empty_pages: 0 },
    errors: [],
    warnings: [],
    artifact_ids: {},
    inventory_sha256: "",
    phase_run_id: "",
  };
}

function emptyTaskQuality(taskId = ""): TaskQuality {
  const empty = { blocking: 0, error: 0, warning: 0, info: 0, decision: "pending" };
  return {
    task_id: taskId,
    current_gate: "pending",
    decision: "pending",
    warning_badge: 0,
    svg_summary: { ...empty },
    pptx_summary: { ...empty },
    findings: [],
    chart_receipts: [],
    text_coverage: 0,
    render_artifact_ids: [],
    contact_sheet_artifact_id: "",
    readback_artifact_id: "",
    allowed_retry_phases: [],
  };
}

function qualityDecisionText(decision: string) {
  switch (decision) {
    case "pass": return "通过";
    case "pass_with_warnings": return "有警告";
    case "fail": return "未通过";
    default: return "等待检查";
  }
}

function fidelityMetricCount(value: number | string[] | undefined) {
  if (typeof value === "number" && Number.isFinite(value)) {
    return value;
  }
  return Array.isArray(value) ? value.length : 0;
}

function beautifyFidelityMetricText(metric?: BeautifyFidelityMetric) {
  if (!metric) {
    return "-";
  }
  const expected = typeof metric.required === "number" ? metric.required : metric.expected || 0;
  const matched = typeof metric.used === "number" ? metric.used : metric.matched || 0;
  const mismatches = fidelityMetricCount(metric.missing)
    + fidelityMetricCount(metric.changed)
    + fidelityMetricCount(metric.reordered)
    + fidelityMetricCount(metric.mismatches);
  return `${matched}/${expected}${mismatches > 0 ? ` · ${mismatches} 异常` : ""}`;
}

function isFullSVGRouteUI(route: string) {
  return route === "main" || route === "beautify";
}

function resourceItemsByStatus(items: TaskResourceItem[]) {
  return {
    ready: items.filter((item) => item.status === "ready"),
    degraded: items.filter((item) => item.status === "degraded" || item.status === "skipped"),
    failed: items.filter((item) => !["ready", "degraded", "skipped"].includes(item.status)),
  };
}

function templateFillText(value: unknown) {
  if (typeof value === "string") {
    return value.trim() || "-";
  }
  if (typeof value === "number") {
    return Number.isFinite(value) ? String(value) : "-";
  }
  if (typeof value === "boolean" || typeof value === "bigint") {
    return String(value);
  }
  return "-";
}

function templateFillSlideRows(plan: Record<string, unknown>) {
  const slides = Array.isArray(plan.slides) ? plan.slides : [];
  return slides.map((item, index) => {
    const slide = item && typeof item === "object" ? item as Record<string, unknown> : {};
    const rationale = slide.layout_rationale && typeof slide.layout_rationale === "object"
      ? slide.layout_rationale as Record<string, unknown>
      : {};
    return {
      index: index + 1,
      sourceSlide: templateFillText(slide.source_slide),
      purpose: templateFillText(slide.purpose),
      layoutPattern: templateFillText(rationale.layout_pattern),
      whyFit: templateFillText(rationale.why_fit),
      risk: templateFillText(rationale.risk),
      notes: typeof slide.notes === "string" && slide.notes.trim() !== "" ? "有" : "无",
      replacements: Array.isArray(slide.replacements) ? slide.replacements.length : 0,
      tableEdits: Array.isArray(slide.table_edits) ? slide.table_edits.length : 0,
      chartEdits: Array.isArray(slide.chart_edits) ? slide.chart_edits.length : 0,
    };
  });
}

function templateFillCheckRows(report: Record<string, unknown>) {
  const results = Array.isArray(report.results) ? report.results : [];
  const normalized = results.flatMap((item) => {
    const row = item && typeof item === "object" ? item as Record<string, unknown> : {};
    const status = typeof row.status === "string" ? row.status.trim().toUpperCase() : "";
    if (status !== "ERROR" && status !== "WARN") {
      return [];
    }
    return [{
      status,
      code: templateFillText(row.code),
      planSlide: templateFillText(row.plan_slide),
      sourceSlide: templateFillText(row.source_slide),
      message: templateFillText(row.message),
    }];
  });
  return [
    ...normalized.filter((row) => row.status === "ERROR"),
    ...normalized.filter((row) => row.status === "WARN"),
  ];
}

function beautifyPlanSlideRows(preview: BeautifyPlanPreview | null) {
  if (!preview) {
    return [];
  }
  const inventoryByPage = new Map(
    (preview.inventory?.pages || []).map((page) => [page.source_slide, page]),
  );
  return (preview.plan?.slides || []).map((slide) => ({
    ...slide,
    inventory: inventoryByPage.get(slide.source_slide),
  }));
}

function beautifyPlanErrorCount(preview: BeautifyPlanPreview | null) {
  return (preview?.findings || []).filter((finding) => ["blocking", "error"].includes(finding.severity)).length;
}

function beautifyPlanActionState({
  canEdit,
  canConfirm,
  taskStatus,
  busy,
  dirty,
  errorCount,
}: {
  canEdit: boolean;
  canConfirm: boolean;
  taskStatus?: TaskStatus;
  busy: boolean;
  dirty: boolean;
  errorCount: number;
}) {
  const hint = dirty
    ? "美化计划已修改，请先保存；页数、顺序、文字和数据始终保持冻结。"
    : errorCount > 0
      ? `存在 ${errorCount} 个阻断问题，请调整布局或风险决策后重新检查。`
      : "";
  return {
    saveDisabled: !canEdit || busy || !dirty,
    checkDisabled: taskStatus !== "awaiting_beautify_confirm" || busy || dirty,
    confirmDisabled: !canConfirm || busy || dirty || errorCount > 0,
    hint,
  };
}

function templateFillActionState({
  canEdit,
  canConfirm,
  taskStatus,
  busy,
  dirty,
  checkErrorCount,
}: {
  canEdit: boolean;
  canConfirm: boolean;
  taskStatus?: TaskStatus;
  busy: boolean;
  dirty: boolean;
  checkErrorCount: number;
  checkWarningCount?: number;
}) {
  const hint = dirty
    ? "JSON 已修改，请先保存后再检查或确认。"
    : checkErrorCount > 0
      ? `存在 ${checkErrorCount} 个检查错误，请修正并保存后再确认。`
      : "";
  return {
    saveDisabled: !canEdit || busy,
    checkDisabled: taskStatus !== "awaiting_template_fill_confirm" || busy || dirty,
    confirmDisabled: !canConfirm || busy || checkErrorCount > 0 || dirty,
    hint,
  };
}

function templateFillBasename(value: unknown) {
  if (typeof value !== "string") {
    return "-";
  }
  const parts = value.trim().replace(/\\/g, "/").split("/").filter(Boolean);
  return parts[parts.length - 1]?.trim() || "-";
}

function templateFillPageKey(taskId: string) {
  return `template-fill:${taskId}`;
}

function beautifyPlanPageKey(taskId: string) {
  return `beautify-plan:${taskId}`;
}

function taskDetailPageKey(taskId: string) {
  return `task-detail:${taskId}`;
}

function taskRouteMatches(route: Route, routeName: "task" | "templateFill" | "beautifyPlan" | "preview", taskId: string) {
  return route.name === routeName && "id" in route && route.id === taskId;
}

function createBeautifyPlanRequestScope(taskId: string, isRouteCurrent: () => boolean = () => true) {
  return createTemplateFillRequestScope(taskId, isRouteCurrent);
}

async function scopedBeautifyPlanRequest<T>(
  scope: ReturnType<typeof createBeautifyPlanRequestScope>,
  currentTaskId: string,
  request: (scopedTaskId: string) => Promise<T>,
  generation = scope.currentGeneration(),
) {
  return scopedTemplateFillRequest(scope, currentTaskId, request, generation);
}

function createTemplateFillRequestScope(taskId: string, isRouteCurrent: () => boolean = () => true) {
  let nextGeneration = 0;
  let activeGeneration = 0;
  return {
    taskId,
    activate() {
      nextGeneration += 1;
      activeGeneration = nextGeneration;
      return activeGeneration;
    },
    deactivate(generation: number) {
      if (activeGeneration === generation) {
        activeGeneration = 0;
      }
    },
    currentGeneration() {
      return activeGeneration;
    },
    isGenerationCurrent(generation: number, currentTaskId: string) {
      return generation !== 0
        && generation === activeGeneration
        && currentTaskId === taskId
        && isRouteCurrent();
    },
    isCurrent(currentTaskId: string) {
      return activeGeneration !== 0 && currentTaskId === taskId && isRouteCurrent();
    },
  };
}

function templateFillScopedTaskID(scope: ReturnType<typeof createTemplateFillRequestScope>, currentTaskId: string) {
  return scope.isCurrent(currentTaskId) ? scope.taskId : "";
}

function startTemplateFillRequestGeneration(
  scope: ReturnType<typeof createTemplateFillRequestScope>,
  run: (generation: number) => void,
) {
  const generation = scope.activate();
  try {
    run(generation);
  } catch (err) {
    scope.deactivate(generation);
    throw err;
  }
  return () => scope.deactivate(generation);
}

async function scopedTemplateFillRequest<T>(
  scope: ReturnType<typeof createTemplateFillRequestScope>,
  currentTaskId: string,
  request: (scopedTaskId: string) => Promise<T>,
  generation = scope.currentGeneration(),
) {
  const scopedTaskId = scope.isGenerationCurrent(generation, currentTaskId) ? scope.taskId : "";
  if (!scopedTaskId) {
    return undefined;
  }
  try {
    const result = await request(scopedTaskId);
    return scope.isGenerationCurrent(generation, currentTaskId) ? result : undefined;
  } catch (err) {
    if (scope.isGenerationCurrent(generation, currentTaskId)) {
      throw err;
    }
    return undefined;
  }
}

function createTaskDetailRequestScope(taskId: string, isRouteCurrent: () => boolean = () => true) {
  let nextGeneration = 0;
  let activeGeneration = 0;
  return {
    taskId,
    activate() {
      nextGeneration += 1;
      activeGeneration = nextGeneration;
      return activeGeneration;
    },
    deactivate() {
      activeGeneration = 0;
    },
    isGenerationCurrent(generation: number, currentTaskId: string) {
      return generation !== 0
        && generation === activeGeneration
        && currentTaskId === taskId
        && isRouteCurrent();
    },
    isCurrent(currentTaskId: string) {
      return activeGeneration !== 0 && currentTaskId === taskId && isRouteCurrent();
    },
  };
}

function templateFillPlanReadableStatus(task?: Pick<Task, "route" | "status">) {
  return !!task
    && task.route === "template-fill"
    && templateFillPlanReadableStatuses.includes(task.status);
}

function beautifyPlanReadableStatus(task?: Pick<Task, "route" | "status">) {
  return !!task && task.route === "beautify" && beautifyPlanStatuses.includes(task.status);
}

async function loadTaskDetailData<
  TTask extends Pick<Task, "id" | "route" | "status">,
  TEvent,
  TArtifact,
  TResources extends { task_id: string },
  TBundle extends { task_id: string },
  TQuality extends { task_id: string },
  TRuntimeRun,
  TPhaseRun,
  TPreview,
  TBeautifyPreview,
>(
  scope: ReturnType<typeof createTaskDetailRequestScope>,
  currentTaskId: string,
  requests: {
    getTask: (id: string) => Promise<TTask>;
    listEvents: (id: string) => Promise<TEvent[]>;
    listArtifacts: (id: string) => Promise<TArtifact[]>;
    getResources: (id: string) => Promise<TResources>;
    getSVGBundle: (id: string) => Promise<TBundle>;
    getQuality: (id: string) => Promise<TQuality>;
    listRuntimeRuns: (id: string) => Promise<TRuntimeRun[]>;
    listPhaseRuns: (id: string) => Promise<TPhaseRun[]>;
    getTemplateFillPlan: (id: string) => Promise<TPreview>;
    getBeautifyPlan?: (id: string) => Promise<TBeautifyPreview>;
  },
) {
  const generation = scope.activate();
  if (!scope.isGenerationCurrent(generation, currentTaskId)) {
    return undefined;
  }
  let core;
  try {
    core = await Promise.all([
      requests.getTask(scope.taskId),
      requests.listEvents(scope.taskId),
      requests.listArtifacts(scope.taskId),
      requests.getResources(scope.taskId),
      requests.getSVGBundle(scope.taskId),
      requests.getQuality(scope.taskId),
      requests.listRuntimeRuns(scope.taskId),
      requests.listPhaseRuns(scope.taskId),
    ]);
  } catch (err) {
    if (scope.isGenerationCurrent(generation, currentTaskId)) {
      throw err;
    }
    return undefined;
  }
  if (!scope.isGenerationCurrent(generation, currentTaskId) || core[0].id !== scope.taskId) {
    return undefined;
  }

  const [task, events, artifacts, resources, svgBundle, quality, runtimeRuns, phaseRuns] = core;
  if (resources.task_id !== scope.taskId || svgBundle.task_id !== scope.taskId || quality.task_id !== scope.taskId) {
    return undefined;
  }
  let templateFillPreview: TPreview | null = null;
  if (templateFillPlanReadableStatus(task)) {
    try {
      templateFillPreview = await requests.getTemplateFillPlan(scope.taskId);
    } catch (err) {
      if (!scope.isGenerationCurrent(generation, currentTaskId)) {
        return undefined;
      }
      templateFillPreview = null;
    }
  }
  if (
    templateFillPreview
    && typeof templateFillPreview === "object"
    && "task_id" in templateFillPreview
    && templateFillPreview.task_id !== scope.taskId
  ) {
    return undefined;
  }
  let beautifyPlanPreview: TBeautifyPreview | null = null;
  if (beautifyPlanReadableStatus(task) && requests.getBeautifyPlan) {
    try {
      beautifyPlanPreview = await requests.getBeautifyPlan(scope.taskId);
    } catch (err) {
      if (!scope.isGenerationCurrent(generation, currentTaskId)) {
        return undefined;
      }
      beautifyPlanPreview = null;
    }
  }
  if (
    beautifyPlanPreview
    && typeof beautifyPlanPreview === "object"
    && "task_id" in beautifyPlanPreview
    && beautifyPlanPreview.task_id !== scope.taskId
  ) {
    return undefined;
  }
  if (!scope.isGenerationCurrent(generation, currentTaskId)) {
    return undefined;
  }
  return {
    task, events, artifacts, resources, svgBundle, quality, runtimeRuns, phaseRuns,
    templateFillPreview, beautifyPlanPreview,
  };
}

function taskDetailRetryTaskID(
  scope: ReturnType<typeof createTaskDetailRequestScope>,
  currentTaskId: string,
  loadedTaskId: string,
) {
  return scope.isCurrent(currentTaskId) && loadedTaskId === scope.taskId ? scope.taskId : "";
}

function retryOptionsForFailure(failurePhase: string, taskRoute = "main"): Array<{ phase: RetryPhase; label: string }> {
  const value = failurePhase.toLowerCase();
  if (taskRoute === "beautify") {
    if (value.startsWith("beautify_inventory.inputs")) {
      return [
        { phase: "prepare", label: "重新准备" },
        { phase: "beautify_inventory", label: "重建美化清单" },
      ];
    }
    if (value.startsWith("beautify_inventory")) {
      return [{ phase: "beautify_inventory", label: "重建美化清单" }];
    }
    if (value.startsWith("beautify_plan") || value.startsWith("beautify_lock")) {
      return [{ phase: "beautify_plan", label: "重建美化计划" }];
    }
  }
  if (taskRoute === "template-fill") {
    if (value.startsWith("template_fill_plan.inputs")) {
      return [
        { phase: "prepare", label: "重新准备" },
        { phase: "template_fill_plan", label: "重建填充计划" },
      ];
    }
    if (value.startsWith("template_fill_plan")) {
      return [{ phase: "template_fill_plan", label: "重建填充计划" }];
    }
    if (value.startsWith("template_fill_check")) {
      return [{ phase: "template_fill_check", label: "重新检查计划" }];
    }
    if (value.startsWith("template_fill_apply")) {
      return [{ phase: "template_fill_apply", label: "重新填充 PPTX" }];
    }
    if (value.startsWith("template_fill_validate")) {
      return [{ phase: "template_fill_validate", label: "重新校验结果" }];
    }
    if (value.startsWith("publish")) {
      return [{ phase: "publish", label: "重新发布" }];
    }
  }
  if (
    value.startsWith("prepare")
    || value.startsWith("source")
    || value.startsWith("route_select")
    || value.startsWith("template_resolve")
  ) {
    return [{ phase: "prepare", label: "重新准备" }];
  }
  if (taskRoute === "template-fill") {
    return [];
  }
  return splitRetryOptions;
}

function retryGuidanceForFailure(failurePhase: string) {
  return failurePhase.toLowerCase().startsWith("template_fill_plan.inputs") ? templateFillPlanInputRecoveryNote : "";
}

function canOpenTemplateFillPlan(task?: Pick<Task, "route" | "status">) {
  return !!task && task.route === "template-fill" && templateFillPlanStatuses.includes(task.status);
}

function canOpenBeautifyPlan(task?: Pick<Task, "route" | "status">) {
  return !!task && task.route === "beautify" && beautifyPlanStatuses.includes(task.status);
}

function completedTaskRoute(taskId: string, taskRoute: string): Route {
  return taskRoute === "template-fill"
    ? { name: "templateFill", id: taskId }
    : { name: "preview", id: taskId };
}

function visibleTaskArtifacts<T>(artifacts: T[], taskRoute: string) {
  return taskRoute === "template-fill" || taskRoute === "beautify" ? artifacts : artifacts.slice(0, 8);
}

async function loadPreviewPageData<TTask extends Pick<Task, "id" | "route">, TArtifact>(
  taskId: string,
  getTask: (id: string) => Promise<TTask>,
  listArtifacts: (id: string) => Promise<TArtifact[]>,
  isActive: () => boolean,
  canonicalize: (route: Route) => void,
) {
  const task = await getTask(taskId);
  if (!isActive()) {
    return null;
  }
  if (task.route === "template-fill") {
    canonicalize({ name: "templateFill", id: task.id });
    return null;
  }
  const artifacts = await listArtifacts(taskId);
  if (!isActive()) {
    return null;
  }
  return { task, artifacts };
}

function previewPageKey(taskId: string) {
  return `preview:${taskId}`;
}

function createPreviewPageState(taskId: string) {
	return {
		taskId,
		task: null as Task | null,
		artifacts: [] as Artifact[],
		versions: [] as ArtifactVersion[],
		selectedVersion: "",
		bundle: emptySVGBundle(taskId),
		sessions: [] as EditSession[],
		activeSession: null as EditSession | null,
		selectedId: "",
		error: "",
	};
}

function previewPageStateForTask(state: ReturnType<typeof createPreviewPageState>, taskId: string) {
  return state.taskId === taskId ? state : createPreviewPageState(taskId);
}

function templateFillNextPhase(status: TaskStatus) {
  switch (status) {
    case "template_fill_planning":
      return "计划审查";
    case "awaiting_template_fill_confirm":
      return "计划检查";
    case "template_fill_checking":
      return "PPTX 填充";
    case "template_fill_applying":
      return "结果校验";
    case "template_fill_validating":
      return "发布产物";
    case "publishing":
      return "完成";
    case "completed":
      return "已完成";
    case "failed":
      return "恢复失败阶段";
    default:
      return "填充计划";
  }
}

function retryPhaseIcon(phase: RetryPhase, active: boolean) {
  if (active) {
    return <Loader2 className="spin" size={16} />;
  }
  switch (phase) {
    case "prepare":
      return <RefreshCw size={16} />;
    case "spec_generate":
      return <FileText size={16} />;
    case "image_acquire":
      return <ImageIcon size={16} />;
    case "template_fill_plan":
      return <FileText size={16} />;
    case "template_fill_check":
      return <ListChecks size={16} />;
    case "template_fill_apply":
      return <Presentation size={16} />;
    case "template_fill_validate":
      return <CheckCircle2 size={16} />;
    case "beautify_inventory":
      return <LayoutList size={16} />;
    case "beautify_plan":
      return <Palette size={16} />;
    case "svg_execute":
      return <Play size={16} />;
    case "quality_check":
      return <CheckCircle2 size={16} />;
    case "finalize_export":
      return <Download size={16} />;
    case "publish":
      return <Upload size={16} />;
  }
}

export function App() {
  const [route, setRoute] = useState<Route>(() => parseRoute());

  useEffect(() => {
    const onHashChange = () => setRoute(parseRoute());
    window.addEventListener("hashchange", onHashChange);
    if (!window.location.hash) {
      go({ name: "tasks" });
    }
    return () => window.removeEventListener("hashchange", onHashChange);
  }, []);

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand-block">
          <div className="brand-mark">
            <Presentation size={18} />
          </div>
          <div>
            <div className="brand-title">SlideSmith</div>
            <div className="brand-subtitle">PPT Master Runtime</div>
          </div>
        </div>
        <nav className="nav-list">
          <button className={route.name === "tasks" ? "nav-item active" : "nav-item"} onClick={() => go({ name: "tasks" })}>
            <LayoutList size={18} />
            <span>任务</span>
          </button>
          <button className={route.name === "new" ? "nav-item active" : "nav-item"} onClick={() => go({ name: "new" })}>
            <Plus size={18} />
            <span>新建</span>
          </button>
        </nav>
      </aside>

      <main className="main-surface">
        {route.name === "tasks" && <TaskListPage />}
        {route.name === "new" && <NewTaskPage />}
        {route.name === "task" && <TaskDetailPage key={taskDetailPageKey(route.id)} taskId={route.id} />}
        {route.name === "confirm" && <ConfirmPage taskId={route.id} />}
        {route.name === "spec" && <SpecPreviewPage taskId={route.id} />}
        {route.name === "templateFill" && (
          <TemplateFillPlanPage key={templateFillPageKey(route.id)} taskId={route.id} />
        )}
        {route.name === "beautifyPlan" && (
          <BeautifyPlanPage key={beautifyPlanPageKey(route.id)} taskId={route.id} />
        )}
        {route.name === "preview" && (
          <PreviewPage key={previewPageKey(route.id)} taskId={route.id} />
        )}
      </main>
    </div>
  );
}

function TaskListPage() {
  const [tasks, setTasks] = useState<Task[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      setTasks(await api.listTasks());
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const stats = useMemo(() => {
    return {
      total: tasks.length,
      active: tasks.filter((task) => activeStatuses.includes(task.status)).length,
      waiting: tasks.filter((task) => isWaitingStatus(task.status)).length,
      completed: tasks.filter((task) => task.status === "completed").length,
    };
  }, [tasks]);

  return (
    <section className="page">
      <PageHeader
        title="任务列表"
        subtitle="Runtime 任务、确认状态和产物出口"
        actions={
          <>
            <IconButton label="刷新" onClick={() => void load()}>
              <RefreshCw size={17} />
            </IconButton>
            <button className="primary-button" onClick={() => go({ name: "new" })}>
              <Plus size={17} />
              <span>新建任务</span>
            </button>
          </>
        }
      />

      <div className="metric-grid">
        <Metric label="全部" value={stats.total} />
        <Metric label="运行中" value={stats.active} />
        <Metric label="待确认" value={stats.waiting} />
        <Metric label="已完成" value={stats.completed} />
      </div>

      <div className="table-surface">
        <div className="table-header task-row">
          <span>标题</span>
          <span>状态</span>
          <span>更新时间</span>
          <span>运行</span>
          <span></span>
        </div>
        {loading && <InlineState icon={<Loader2 className="spin" size={18} />} text="加载中" />}
        {!loading && error && <InlineState icon={<XCircle size={18} />} text={error} bad />}
        {!loading && !error && tasks.length === 0 && (
          <div className="empty-state">
            <FileText size={24} />
            <span>暂无任务</span>
            <button className="secondary-button" onClick={() => go({ name: "new" })}>
              <Plus size={16} />
              <span>新建任务</span>
            </button>
          </div>
        )}
        {!loading &&
          !error &&
          tasks.map((task) => (
            <button className="task-row task-row-button" key={task.id} onClick={() => go({ name: "task", id: task.id })}>
              <span className="task-title-cell">
                <FileText size={16} />
                <span>{task.title}</span>
              </span>
              <StatusPill status={task.status} />
              <span className="muted">{formatTime(task.updated_at)}</span>
              <span className="mono small">{task.last_runtime_run_id || "-"}</span>
              <span className="row-actions">
                {isConfirmationStatus(task.status) && <ListChecks size={16} />}
                {task.status === "awaiting_spec_confirm" && <FileText size={16} />}
                {task.status === "completed" && <Eye size={16} />}
              </span>
            </button>
          ))}
      </div>
    </section>
  );
}

function NewTaskPage() {
  const [title, setTitle] = useState("");
  const [files, setFiles] = useState<File[]>([]);
  const [templates, setTemplates] = useState<TemplateCatalogItem[]>([]);
  const [templateLoading, setTemplateLoading] = useState(true);
  const [templateError, setTemplateError] = useState("");
  const [selectedTemplateId, setSelectedTemplateId] = useState(() => readStoredTemplateID());
  const [kindFilter, setKindFilter] = useState("all");
  const [canvasFilter, setCanvasFilter] = useState("all");
  const [pageCountFilter, setPageCountFilter] = useState("all");
  const [templateQuery, setTemplateQuery] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [stage, setStage] = useState("");

  useEffect(() => {
    let cancelled = false;
    async function loadTemplates() {
      setTemplateLoading(true);
      setTemplateError("");
      try {
        const nextTemplates = await api.listTemplates();
        if (cancelled) {
          return;
        }
        setTemplates(nextTemplates);
        setSelectedTemplateId((current) => {
          if (current && nextTemplates.some((template) => template.id === current)) {
            return current;
          }
          const nextID = nextTemplates[0]?.id || "";
          writeStoredTemplateID(nextID);
          return nextID;
        });
      } catch (err) {
        if (!cancelled) {
          setTemplateError(err instanceof Error ? err.message : String(err));
        }
      } finally {
        if (!cancelled) {
          setTemplateLoading(false);
        }
      }
    }
    void loadTemplates();
    return () => {
      cancelled = true;
    };
  }, []);

  const canvasOptions = useMemo(() => uniqueTemplateValues(templates.map((template) => template.canvas)), [templates]);
  const pageCountOptions = useMemo(
    () => uniqueTemplateValues(templates.map((template) => (template.default_page_count ? String(template.default_page_count) : ""))),
    [templates],
  );
  const filteredTemplates = useMemo(() => {
    const query = templateQuery.trim().toLowerCase();
    return templates.filter((template) => {
      if (kindFilter !== "all" && template.kind !== kindFilter) {
        return false;
      }
      if (canvasFilter !== "all" && template.canvas !== canvasFilter) {
        return false;
      }
      if (pageCountFilter !== "all" && String(template.default_page_count || "") !== pageCountFilter) {
        return false;
      }
      if (!query) {
        return true;
      }
      const haystack = [template.display_name, template.name, template.kind, template.summary, template.canvas, template.primary_color]
        .filter(Boolean)
        .join(" ")
        .toLowerCase();
      return haystack.includes(query);
    });
  }, [canvasFilter, kindFilter, pageCountFilter, templateQuery, templates]);
  const selectedTemplate = templates.find((template) => template.id === selectedTemplateId) || null;

  function selectTemplate(templateID: string) {
    setSelectedTemplateId(templateID);
    writeStoredTemplateID(templateID);
  }

  async function submit() {
    if (files.length === 0 || busy || !selectedTemplate) {
      return;
    }
    setBusy(true);
    setError("");
    try {
      setStage("创建任务");
      const task = await api.createTask(title, selectedTemplate.id);
      for (const sourceFile of files) {
        setStage(`上传资料：${sourceFile.name}`);
        await api.uploadFile(task.id, sourceFile);
      }
      setStage("启动运行层");
      const started = await api.startTask(task.id);
      writeStoredTemplateID(selectedTemplate.id);
      if (isConfirmationStatus(started.status)) {
        go({ name: "confirm", id: started.id });
      } else {
        go({ name: "task", id: started.id });
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
      setStage("");
    }
  }

  return (
    <section className="page">
      <PageHeader
        title="新建任务"
        subtitle="上传资料、选择模板并启动 PPT Master prepare 阶段"
        actions={
          <button className="secondary-button" onClick={() => go({ name: "tasks" })}>
            <ArrowLeft size={16} />
            <span>返回</span>
          </button>
        }
      />

      <div className="new-task-layout">
        <div className="form-surface new-task-form">
          <label className="field">
            <span>任务标题</span>
            <input value={title} onChange={(event) => setTitle(event.target.value)} placeholder="例如：MVP 运行层验证汇报" />
          </label>

          <label className={files.length > 0 ? "upload-zone has-file" : "upload-zone"}>
            <FileUp size={28} />
            <input
              type="file"
              multiple
              accept={supportedSourceAccept}
              onChange={(event) => setFiles(Array.from(event.target.files ?? []))}
            />
            <span>{files.length > 0 ? `${files.length} 个文件已选择` : "选择 Markdown / PDF / Office / PPTX 文件"}</span>
            {files.length > 0 && <small>{files.map((sourceFile) => sourceFile.name).join(" · ")}</small>}
          </label>

          <div className="selected-template-strip">
            <div>
              <span>当前模板</span>
              <strong>{selectedTemplate ? selectedTemplate.display_name : "未选择"}</strong>
            </div>
            {selectedTemplate && <TemplateMeta template={selectedTemplate} compact />}
          </div>

          {error && <InlineState icon={<XCircle size={18} />} text={error} bad />}
          <button className="primary-button wide" disabled={files.length === 0 || !selectedTemplate || busy} onClick={() => void submit()}>
            {busy ? <Loader2 className="spin" size={17} /> : <Upload size={17} />}
            <span>{busy ? stage : "创建并启动"}</span>
          </button>
        </div>

        <div className="template-surface">
          <div className="template-toolbar">
            <div className="section-title">
              <Layers size={17} />
              <span>模板</span>
            </div>
            <label className="template-search">
              <Search size={16} />
              <input value={templateQuery} onChange={(event) => setTemplateQuery(event.target.value)} placeholder="搜索模板" />
            </label>
          </div>

          <div className="template-filter-row">
            <div className="segmented compact">
              {templateKindFilters.map((option) => (
                <button
                  key={option.value}
                  className={kindFilter === option.value ? "segment active" : "segment"}
                  onClick={() => setKindFilter(option.value)}
                  type="button"
                >
                  {option.label}
                </button>
              ))}
            </div>
            <label className="select-field">
              <span>画布</span>
              <select value={canvasFilter} onChange={(event) => setCanvasFilter(event.target.value)}>
                <option value="all">全部</option>
                {canvasOptions.map((canvas) => (
                  <option value={canvas} key={canvas}>
                    {canvas}
                  </option>
                ))}
              </select>
            </label>
            <label className="select-field">
              <span>页数</span>
              <select value={pageCountFilter} onChange={(event) => setPageCountFilter(event.target.value)}>
                <option value="all">全部</option>
                {pageCountOptions.map((pageCount) => (
                  <option value={pageCount} key={pageCount}>
                    {pageCount}
                  </option>
                ))}
              </select>
            </label>
          </div>

          {templateLoading && <InlineState icon={<Loader2 className="spin" size={18} />} text="加载模板" />}
          {!templateLoading && templateError && <InlineState icon={<XCircle size={18} />} text={templateError} bad />}
          {!templateLoading && !templateError && filteredTemplates.length === 0 && <InlineState icon={<ImageIcon size={18} />} text="无匹配模板" />}
          {!templateLoading && !templateError && filteredTemplates.length > 0 && (
            <div className="template-grid">
              {filteredTemplates.map((template) => (
                <TemplateCard
                  key={template.id}
                  template={template}
                  selected={template.id === selectedTemplateId}
                  onSelect={() => selectTemplate(template.id)}
                />
              ))}
            </div>
          )}
        </div>
      </div>
    </section>
  );
}

function TemplateCard({
  template,
  selected,
  onSelect,
}: {
  template: TemplateCatalogItem;
  selected: boolean;
  onSelect: () => void;
}) {
  const preview = bestTemplatePreview(template);
  return (
    <button className={selected ? "template-card active" : "template-card"} onClick={onSelect} type="button">
      <div className="template-preview-frame">
        {preview ? (
          <img alt={`${template.display_name} preview`} src={api.templateAssetUrl(preview.url)} />
        ) : (
          <div className="template-preview-empty">
            <ImageIcon size={24} />
          </div>
        )}
        {selected && (
          <span className="template-selected-mark">
            <CheckCircle2 size={16} />
          </span>
        )}
      </div>
      <div className="template-card-body">
        <div className="template-title-row">
          <span className="template-kind-badge">{templateKindLabel(template.kind)}</span>
          <span className="template-id mono">{template.id}</span>
        </div>
        <strong>{template.display_name}</strong>
        {template.summary && <p>{template.summary}</p>}
        <TemplateMeta template={template} />
      </div>
    </button>
  );
}

function TemplateMeta({ template, compact = false }: { template: TemplateCatalogItem; compact?: boolean }) {
  return (
    <div className={compact ? "template-meta compact" : "template-meta"}>
      <span>
        <Layers size={13} />
        {templateKindLabel(template.kind)}
      </span>
      {template.canvas && (
        <span>
          <Presentation size={13} />
          {template.canvas}
        </span>
      )}
      {!!template.default_page_count && (
        <span>
          <FileText size={13} />
          {template.default_page_count} 页
        </span>
      )}
      {template.primary_color && (
        <span>
          <Palette size={13} />
          <i className="template-color-swatch" style={{ background: template.primary_color }} />
          {template.primary_color}
        </span>
      )}
    </div>
  );
}

function bestTemplatePreview(template: TemplateCatalogItem) {
  const previews = template.preview_assets || [];
  return (
    previews.find((preview) => preview.name === "cover") ||
    previews.find((preview) => preview.path.includes("01_")) ||
    previews[0]
  );
}

function templateKindLabel(kind: string) {
  switch (kind) {
    case "layout":
      return "版式";
    case "deck":
      return "成套";
    case "brand":
      return "品牌";
    default:
      return kind;
  }
}

function uniqueTemplateValues(values: Array<string | undefined>) {
  return [...new Set(values.map((value) => value?.trim()).filter((value): value is string => !!value))].sort();
}

function readStoredTemplateID() {
  try {
    return window.sessionStorage.getItem(templateSelectionKey) || "";
  } catch {
    return "";
  }
}

function writeStoredTemplateID(templateID: string) {
  try {
    if (templateID) {
      window.sessionStorage.setItem(templateSelectionKey, templateID);
    } else {
      window.sessionStorage.removeItem(templateSelectionKey);
    }
  } catch {
    // Ignore storage failures in private browsing contexts.
  }
}

function TaskDetailPage({ taskId }: { taskId: string }) {
  const [requestScope] = useState(() => createTaskDetailRequestScope(
    taskId,
    () => taskRouteMatches(parseRoute(), "task", taskId),
  ));
  const [detail, setDetail] = useState(() => ({
    task: null as Task | null,
    events: [] as TaskEvent[],
    artifacts: [] as Artifact[],
    resources: emptyTaskResources(taskId),
    svgBundle: emptySVGBundle(taskId),
    quality: emptyTaskQuality(taskId),
    runtimeRuns: [] as RuntimeRun[],
    phaseRuns: [] as TaskPhaseRun[],
    templateFillPreview: null as TemplateFillPlanPreview | null,
    beautifyPlanPreview: null as BeautifyPlanPreview | null,
  }));
  const [retrying, setRetrying] = useState<RetryPhase | "">("");
  const [error, setError] = useState("");
  const {
    task, events, artifacts, resources, svgBundle, quality, runtimeRuns, phaseRuns,
    templateFillPreview, beautifyPlanPreview,
  } = detail;

  const load = useCallback(async () => {
    try {
      const next = await loadTaskDetailData(requestScope, taskId, {
        getTask: api.getTask,
        listEvents: api.listEvents,
        listArtifacts: api.listArtifacts,
        getResources: api.getResources,
        getSVGBundle: api.getSVGBundle,
        getQuality: api.getQuality,
        listRuntimeRuns: api.listRuntimeRuns,
        listPhaseRuns: api.listPhaseRuns,
        getTemplateFillPlan: api.getTemplateFillPlan,
        getBeautifyPlan: api.getBeautifyPlan,
      });
      if (next) {
        setDetail(next);
        setError("");
      }
    } catch (err) {
      if (requestScope.isCurrent(taskId)) {
        setError(err instanceof Error ? err.message : String(err));
      }
    }
  }, [requestScope, taskId]);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 2500);
    return () => {
      window.clearInterval(timer);
      requestScope.deactivate();
    };
  }, [load, requestScope]);

  const pptx = artifacts.find((artifact) => artifact.kind === "pptx");
  const svgFinalCount = artifacts.filter((artifact) => artifact.kind === "svg_final").length;
  const latestRun = runtimeRuns[0];
  const failureMetadata = task ? parseJSON<Record<string, unknown>>(task.failure_metadata || "{}", {}) : {};
  const preflightReport = failureMetadata.preflight && typeof failureMetadata.preflight === "object"
    ? failureMetadata.preflight as Record<string, unknown>
    : {};
  const preflightChecks = Array.isArray(preflightReport.checks)
    ? preflightReport.checks.filter((check): check is Record<string, unknown> => !!check && typeof check === "object")
    : [];
  const failedPreflightChecks = preflightChecks.filter((check) => check.status === "error");
  const routeSelection = task ? parseJSON<Record<string, unknown>>(task.route_selection_json || "{}", {}) : {};
  const sourcePrepareRun = [...phaseRuns].reverse().find((run) => run.phase === "source_prepare");
  const sourcePrepareOutput = sourcePrepareRun
    ? parseJSON<Record<string, unknown>>(sourcePrepareRun.output_json || "{}", {})
    : {};
  const sourceContract = sourcePrepareOutput.source_contract as Record<string, unknown> | undefined;
  const taskRoute = task?.route || "main";
  const fullSVGRoute = isFullSVGRouteUI(taskRoute);
  const routeConfidence = typeof routeSelection.confidence === "number" ? Math.round(routeSelection.confidence * 100) : null;
  const retryOptions = task?.status === "failed" ? retryOptionsForFailure(task.failure_phase || "", taskRoute) : [];
  const retryGuidance = task?.status === "failed" ? retryGuidanceForFailure(task.failure_phase || "") : "";
  const displayedArtifacts = visibleTaskArtifacts(artifacts, taskRoute);
  const resourceGroups = resourceItemsByStatus(resources.resources);
  const qualityBlocking = quality.svg_summary.blocking
    + quality.pptx_summary.blocking
    + (quality.beautify_fidelity?.blocking || 0);
  const qualityErrors = quality.svg_summary.error
    + quality.pptx_summary.error
    + (quality.beautify_fidelity?.error || 0);

  async function retry(phase: RetryPhase) {
    const loadedTaskId = task?.id || "";
    const retryTaskId = taskDetailRetryTaskID(requestScope, taskId, loadedTaskId);
    if (!retryTaskId || retrying) {
      return;
    }
    setRetrying(phase);
    setError("");
    try {
      await api.retryTask(retryTaskId, phase);
      if (taskDetailRetryTaskID(requestScope, taskId, loadedTaskId) === retryTaskId) {
        await load();
      }
    } catch (err) {
      if (taskDetailRetryTaskID(requestScope, taskId, loadedTaskId) === retryTaskId) {
        setError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      if (taskDetailRetryTaskID(requestScope, taskId, loadedTaskId) === retryTaskId) {
        setRetrying("");
      }
    }
  }

  return (
    <section className="page">
      <PageHeader
        title={task?.title || "任务详情"}
        subtitle={task ? task.id : taskId}
        actions={
          <>
            <button className="secondary-button" onClick={() => go({ name: "tasks" })}>
              <ArrowLeft size={16} />
              <span>返回</span>
            </button>
            {task && isConfirmationStatus(task.status) && (
              <button className="primary-button" onClick={() => go({ name: "confirm", id: task.id })}>
                <ListChecks size={17} />
                <span>确认</span>
              </button>
            )}
            {task?.status === "awaiting_spec_confirm" && (
              <button className="primary-button" onClick={() => go({ name: "spec", id: task.id })}>
                <FileText size={17} />
                <span>审查规格</span>
              </button>
            )}
            {task?.status === "completed" && task.route !== "template-fill" && (
              <button className="primary-button" onClick={() => go(completedTaskRoute(task.id, task.route))}>
                <Eye size={17} />
                <span>预览</span>
              </button>
            )}
            {task && canOpenTemplateFillPlan(task) && (
              <button className="primary-button" onClick={() => go({ name: "templateFill", id: task.id })}>
                <ListChecks size={17} />
                <span>打开填充计划</span>
              </button>
            )}
            {task && canOpenBeautifyPlan(task) && (
              <button className="primary-button" onClick={() => go({ name: "beautifyPlan", id: task.id })}>
                <Palette size={17} />
                <span>打开美化计划</span>
              </button>
            )}
          </>
        }
      />

      {error && <InlineState icon={<XCircle size={18} />} text={error} bad />}
      {task && (
        <>
          <div className="detail-grid">
            <div className="status-panel">
              <div className="section-title">
                <Activity size={17} />
                <span>状态</span>
              </div>
              <StatusPill status={task.status} large />
              <div className="kv-grid">
                <span>运行项目</span>
                <strong className="mono">{task.runtime_project}</strong>
                <span>模板</span>
                <strong className="mono">{task.selected_template_id || "-"}</strong>
                <span>生成路线</span>
                <strong>{routeLabel[taskRoute] || taskRoute}</strong>
                <span>生成引擎</span>
                <strong>{taskRunnerProfileLabel(task.runner_profile || "", taskRoute)}</strong>
                <span>引擎来源</span>
                <strong>{runnerProfileSourceLabel[task.runner_profile_source || ""] || task.runner_profile_source || "-"}</strong>
                <span>锁定状态</span>
                <strong>{task.runner_profile_locked_at ? `已锁定 · ${formatTime(task.runner_profile_locked_at)}` : "未锁定"}</strong>
                {!task.runner_profile_locked_at && !["created", "uploaded"].includes(task.status) && (
                  <>
                    <span>引擎异常</span>
                    <strong className="bad">任务已进入运行阶段但引擎尚未锁定</strong>
                  </>
                )}
                <span>路线原因</span>
                <strong>{task.route_reason || "-"}</strong>
                <span>独立工作流</span>
                <strong className="mono">{task.route_standalone_workflow || "-"}</strong>
                {routeConfidence !== null && (
                  <>
                    <span>路线置信度</span>
                    <strong>{routeConfidence}%</strong>
                  </>
                )}
                <span>最后运行</span>
                <strong className="mono">{task.last_runtime_run_id || "-"}</strong>
                <span>SVG 预览</span>
                <strong>{svgFinalCount}</strong>
                <span>PPTX</span>
                <strong>{pptx ? formatBytes(pptx.size) : "-"}</strong>
                <span>发布版本</span>
                <strong className="mono">{pptx?.publish_version || "-"}</strong>
                <span>质量门禁</span>
                <strong className={quality.decision === "fail" ? "bad" : ""}>
                  {qualityDecisionText(quality.decision)}{quality.warning_badge > 0 ? ` · ${quality.warning_badge} 警告` : ""}
                </strong>
                {task.status === "failed" && (
                  <>
                    <span>失败阶段</span>
                    <strong className="mono">{task.failure_phase || "-"}</strong>
                  </>
                )}
              </div>
            </div>

            {fullSVGRoute && (
              <div className="status-panel resource-panel">
                <div className="section-title">
                  <ImageIcon size={17} />
                  <span>资源准备</span>
                </div>
                <div className="resource-summary-grid">
                  <span>总数<strong>{resources.summary.total}</strong></span>
                  <span>可用<strong>{resources.summary.ready}</strong></span>
                  <span>降级<strong>{resources.summary.degraded}</strong></span>
                  <span>失败<strong>{resources.summary.failed + resources.summary.pending}</strong></span>
                  <span>体积<strong>{formatBytes(resources.summary.bytes)}</strong></span>
                </div>
                <div className="kv-grid compact">
                  <span>阶段状态</span>
                  <strong className="mono">{resources.phase_status || "-"}</strong>
                  <span>Manifest</span>
                  <strong className="mono">{resources.manifest_sha256 ? resources.manifest_sha256.slice(0, 12) : "-"}</strong>
                </div>
                <div className="resource-groups">
                  {([
                    ["ready", "可用", resourceGroups.ready],
                    ["degraded", "已降级", resourceGroups.degraded],
                    ["failed", "失败 / 待处理", resourceGroups.failed],
                  ] as const).map(([key, label, items]) => (
                    <div className={`resource-group ${key}`} key={key}>
                      <div className="resource-group-title">
                        <span>{label}</span>
                        <strong>{items.length}</strong>
                      </div>
                      {items.map((item) => (
                        <div className="resource-item" key={item.id}>
                          <div>
                            <strong className="mono">{item.id}</strong>
                            <span>{item.purpose || item.type} · 第 {item.page} 页</span>
                          </div>
                          <div className="resource-item-meta">
                            <span>{item.status}</span>
                            {item.fallback?.type && <span>{item.fallback.type}: {item.fallback.reason || "已批准降级"}</span>}
                            {item.error_code && <span className="bad">{item.error_code}{item.error ? ` · ${item.error}` : ""}</span>}
                            {item.artifact_id && (
                              <a
                                className="resource-preview-link"
                                href={api.artifactContentUrl(task.id, item.artifact_id)}
                                target="_blank"
                                rel="noreferrer"
                              >
                                预览
                              </a>
                            )}
                          </div>
                        </div>
                      ))}
                    </div>
                  ))}
                  {resources.resources.length === 0 && <span className="muted">尚无资源清单</span>}
                </div>
                {task.status === "failed" && task.failure_phase.toLowerCase().startsWith("image_acquire") && (
                  <div className="button-row left">
                    <button className="secondary-button" disabled={!!retrying} onClick={() => void retry("image_acquire")}>
                      {retryPhaseIcon("image_acquire", retrying === "image_acquire")}
                      <span>重试资源准备</span>
                    </button>
                  </div>
                )}
              </div>
            )}

            {fullSVGRoute && (
              <div className="status-panel executor-panel">
                <div className="section-title">
                  <Layers size={17} />
                  <span>Executor / SVG 契约</span>
                </div>
                <div className="resource-summary-grid executor-summary-grid">
                  <span>页数<strong>{svgBundle.page_count}</strong></span>
                  <span>画布<strong>{svgBundle.canvas.id || "-"}</strong></span>
                  <span>资源绑定<strong>{svgBundle.resource_summary.bindings || 0}</strong></span>
                  <span>图表<strong>{svgBundle.chart_summary.charts || 0}</strong></span>
                  <span>讲稿<strong>{svgBundle.notes.present ? `${svgBundle.notes.page_count} 页` : "-"}</strong></span>
                </div>
                <div className="kv-grid compact">
                  <span>契约状态</span>
                  <strong className={svgBundle.errors.length ? "bad" : ""}>
                    {svgBundle.passed ? "已通过" : svgBundle.phase_status || "等待生成"}
                  </strong>
                  <span>Inventory</span>
                  <strong className="mono">{svgBundle.inventory_sha256 ? svgBundle.inventory_sha256.slice(0, 12) : "-"}</strong>
                  <span>结构错误</span>
                  <strong className={svgBundle.errors.length ? "bad" : ""}>{svgBundle.errors.join(" · ") || "0"}</strong>
                  <span>警告</span>
                  <strong>{svgBundle.warnings.length}</strong>
                </div>
                <div className="executor-page-list">
                  {svgBundle.pages.map((page) => (
                    <div className="executor-page-row" key={page.page_id}>
                      <strong className="mono">{page.page_id}</strong>
                      <span className="mono">{page.filename}</span>
                      <span>文本 {page.text_count}</span>
                      <span>图片 {page.image_count}</span>
                      <span>图表 {page.chart_count}</span>
                      <span>资源 {page.resource_count}</span>
                      <span>{page.notes_present ? "讲稿 ✓" : "讲稿缺失"}</span>
                    </div>
                  ))}
                  {svgBundle.pages.length === 0 && <span className="muted">尚无已通过契约的 SVG bundle</span>}
                </div>
                {task.status === "failed" && task.failure_phase.toLowerCase().startsWith("svg_execute") && (
                  <div className="button-row left">
                    <button className="secondary-button" disabled={!!retrying} onClick={() => void retry("svg_execute")}>
                      {retryPhaseIcon("svg_execute", retrying === "svg_execute")}
                      <span>重试 SVG</span>
                    </button>
                  </div>
                )}
              </div>
            )}

            {fullSVGRoute && (
              <div className={`status-panel quality-panel quality-${quality.decision}`}>
                <div className="section-title">
                  <CheckCircle2 size={17} />
                  <span>生成质量门禁</span>
                  {quality.warning_badge > 0 && <span className="quality-warning-badge">{quality.warning_badge}</span>}
                </div>
                <div className="resource-summary-grid quality-summary-grid">
                  <span>总体<strong>{qualityDecisionText(quality.decision)}</strong></span>
                  <span>SVG<strong>{qualityDecisionText(quality.svg_summary.decision)}</strong></span>
                  <span>图表<strong>{quality.chart_receipts.length}</strong></span>
                  <span>PPTX<strong>{qualityDecisionText(quality.pptx_summary.decision)}</strong></span>
                  <span>文本覆盖<strong>{quality.text_coverage > 0 ? `${Math.round(quality.text_coverage * 100)}%` : "-"}</strong></span>
                  {taskRoute === "beautify" && (
                    <span>源内容保真<strong>{qualityDecisionText(quality.beautify_fidelity?.decision || "pending")}</strong></span>
                  )}
                </div>
                <div className="kv-grid compact">
                  <span>当前门禁</span>
                  <strong className="mono">{phaseLabel[quality.current_gate] || quality.current_gate}</strong>
                  <span>阻断 / 错误</span>
                  <strong className={(qualityBlocking + qualityErrors) > 0 ? "bad" : ""}>
                    {qualityBlocking} / {qualityErrors}
                  </strong>
                  <span>警告</span>
                  <strong>{quality.warning_badge}</strong>
                </div>
                {quality.chart_receipts.length > 0 && (
                  <div className="quality-receipts">
                    {quality.chart_receipts.map((receipt) => (
                      <span key={`${receipt.page_id}:${receipt.chart_id}`}>
                        <strong>{receipt.page_id}</strong> {receipt.chart_id} · {receipt.mode} · {qualityDecisionText(receipt.decision)}
                      </span>
                    ))}
                  </div>
                )}
                <div className="quality-findings">
                  {quality.findings.map((finding) => (
                    <div className={`quality-finding severity-${finding.severity}`} key={finding.id}>
                      <span className="quality-severity">{finding.severity}</span>
                      <strong>{finding.page_id || "Deck"} · {finding.rule}</strong>
                      <span>{finding.message}</span>
                      <small>{finding.retry_phase ? `建议重试：${phaseLabel[finding.retry_phase] || finding.retry_phase}` : finding.artifact}</small>
                    </div>
                  ))}
                  {quality.findings.length === 0 && <span className="muted">尚无质量问题</span>}
                </div>
                <div className="button-row left quality-artifact-actions">
                  {quality.contact_sheet_artifact_id && (
                    <a className="secondary-button" href={api.artifactContentUrl(task.id, quality.contact_sheet_artifact_id)} target="_blank" rel="noreferrer">
                      <Eye size={16} /><span>联系表</span>
                    </a>
                  )}
                  {quality.readback_artifact_id && (
                    <a className="secondary-button" href={api.artifactContentUrl(task.id, quality.readback_artifact_id)} target="_blank" rel="noreferrer">
                      <FileText size={16} /><span>PPTX 回读</span>
                    </a>
                  )}
                  {quality.render_artifact_ids.slice(0, 6).map((artifactId, index) => (
                    <a className="secondary-button compact-button" href={api.artifactContentUrl(task.id, artifactId)} target="_blank" rel="noreferrer" key={artifactId}>
                      P{String(index + 1).padStart(2, "0")}
                    </a>
                  ))}
                </div>
                {taskRoute === "beautify" && quality.beautify_fidelity && (
                  <section className="beautify-fidelity" aria-labelledby="beautify-fidelity-title">
                    <div className="section-title" id="beautify-fidelity-title">
                      <Layers size={16} />
                      <span>源 PPTX → 输出 PPTX 保真</span>
                    </div>
                    <div className="resource-summary-grid quality-summary-grid">
                      <span>源页数<strong>{quality.beautify_fidelity.source_slide_count}</strong></span>
                      <span>输出页数<strong>{quality.beautify_fidelity.output_slide_count}</strong></span>
                      <span>结果<strong>{qualityDecisionText(quality.beautify_fidelity.decision)}</strong></span>
                      <span>覆盖页面<strong>{quality.beautify_fidelity.pages.length}</strong></span>
                      <span>视觉覆盖<strong>{quality.beautify_fidelity.identity.overrides.length}</strong></span>
                    </div>
                    <div className="beautify-fidelity-pages">
                      {quality.beautify_fidelity.pages.map((page) => (
                        <div className={`beautify-fidelity-row fidelity-${page.decision}`} key={`${page.source_slide}:${page.output_page}`}>
                          <strong>P{String(page.source_slide).padStart(2, "0")} → P{String(page.output_page).padStart(2, "0")}</strong>
                          <span>文字 {beautifyFidelityMetricText(page.text)}</span>
                          <span>表格 {beautifyFidelityMetricText(page.tables)}</span>
                          <span>图表 {beautifyFidelityMetricText(page.charts)}</span>
                          <span>图片 {beautifyFidelityMetricText(page.images)}</span>
                          <span>{qualityDecisionText(page.decision)}</span>
                        </div>
                      ))}
                      {quality.beautify_fidelity.pages.length === 0 && <span className="muted">尚无逐页保真结果</span>}
                    </div>
                    <div className="kv-grid compact">
                      <span>身份来源</span>
                      <strong>{quality.beautify_fidelity.identity.selected_source || "-"}</strong>
                      <span>字体替换</span>
                      <strong>{quality.beautify_fidelity.identity.font_substitutions.join(" · ") || "0"}</strong>
                      <span>忽略 / 不支持</span>
                      <strong>{quality.beautify_fidelity.ignored.length} / {quality.beautify_fidelity.unsupported.length}</strong>
                    </div>
                    {quality.beautify_fidelity.report_artifact_id && (
                      <div className="button-row left">
                        <a
                          className="secondary-button"
                          href={api.artifactContentUrl(task.id, quality.beautify_fidelity.report_artifact_id)}
                          target="_blank"
                          rel="noreferrer"
                        >
                          <FileText size={16} /><span>美化保真报告</span>
                        </a>
                      </div>
                    )}
                  </section>
                )}
              </div>
            )}

            <div className="status-panel">
              <div className="section-title">
                <FileText size={17} />
                <span>资料解析</span>
              </div>
              <div className="kv-grid">
                <span>源文件</span>
                <strong>
                  {typeof sourceContract?.source_count === "number" && sourceContract.source_count !== 0
                    ? sourceContract.source_count
                    : "-"}
                </strong>
                <span>转换文本</span>
                <strong>
                  {typeof sourceContract?.normalized_markdown_count === "number" && sourceContract.normalized_markdown_count !== 0
                    ? sourceContract.normalized_markdown_count
                    : "-"}
                </strong>
                <span>PPTX 分析</span>
                <strong>{sourceContract?.has_source_profile ? "已生成" : "-"}</strong>
                <span>分析目录</span>
                <strong className="mono">
                  {typeof sourceContract?.source_profile === "string" && sourceContract.source_profile.trim() !== ""
                    ? sourceContract.source_profile.trim()
                    : "-"}
                </strong>
              </div>
            </div>

            <div className="status-panel">
              <div className="section-title">
                <Presentation size={17} />
                <span>产物</span>
              </div>
              <div className="artifact-list">
                {displayedArtifacts.map((artifact) => (
                  <span className="artifact-chip" key={artifact.id}>
                    {artifactKindLabel[artifact.kind] || artifact.kind}
                    <small>{artifact.name}</small>
                  </span>
                ))}
                {artifacts.length === 0 && <span className="muted">-</span>}
              </div>
              <div className="button-row">
                {taskRoute !== "template-fill" && (
                  <button className="secondary-button" disabled={svgFinalCount === 0} onClick={() => go({ name: "preview", id: task.id })}>
                    <Eye size={16} />
                    <span>SVG</span>
                  </button>
                )}
                <a className={pptx ? "secondary-button" : "secondary-button disabled"} href={pptx ? api.pptxDownloadUrl(task.id) : undefined}>
                  <Download size={16} />
                  <span>PPTX</span>
                </a>
              </div>
            </div>

            {task.status === "awaiting_spec_confirm" && (
              <div className="status-panel spec-gate-panel">
                <div className="section-title">
                  <FileText size={17} />
                  <span>规格审查</span>
                </div>
                <div className="kv-grid">
                  <span>下一阶段</span>
                  <strong>SVG 生成</strong>
                  <span>门禁阶段</span>
                  <strong className="mono">spec_generate</strong>
                  <span>Workspace</span>
                  <strong className="mono">{task.runtime_workspace_path || "-"}</strong>
                </div>
                <div className="button-row left">
                  <button className="primary-button" onClick={() => go({ name: "spec", id: task.id })}>
                    <FileText size={16} />
                    <span>打开规格</span>
                  </button>
                </div>
              </div>
            )}

            {taskRoute === "template-fill" && (
              <div className="status-panel template-fill-gate-panel">
                <div className="section-title">
                  <ListChecks size={17} />
                  <span>模板填充</span>
                </div>
                <div className="kv-grid">
                  <span>下一阶段</span>
                  <strong>{templateFillNextPhase(task.status)}</strong>
                  <span className="mono">analysis/fill_plan.json</span>
                  <strong>{templateFillPreview ? "已生成" : "-"}</strong>
                  <span>检查错误</span>
                  <strong>{templateFillPreview ? numberFromSummary(templateFillPreview.summary.check_error) : "-"}</strong>
                  <span>检查警告</span>
                  <strong>{templateFillPreview ? numberFromSummary(templateFillPreview.summary.check_warn) : "-"}</strong>
                  <span>输出页数</span>
                  <strong>{templateFillPreview ? numberFromSummary(templateFillPreview.summary.planned_slide_count) : "-"}</strong>
                  <span>Workspace</span>
                  <strong className="mono">{task.runtime_workspace_path || "-"}</strong>
                </div>
                {canOpenTemplateFillPlan(task) && (
                  <div className="button-row left">
                    <button className="primary-button" onClick={() => go({ name: "templateFill", id: task.id })}>
                      <FileText size={16} />
                      <span>打开填充计划</span>
                    </button>
                  </div>
                )}
              </div>
            )}

            {taskRoute === "beautify" && (
              <div className="status-panel beautify-gate-panel">
                <div className="section-title">
                  <Palette size={17} />
                  <span>PPTX 美化</span>
                </div>
                <div className="kv-grid">
                  <span>源文件</span>
                  <strong>{beautifyPlanPreview?.source?.name || "-"}</strong>
                  <span>页数锁定</span>
                  <strong>{beautifyPlanPreview?.source?.slide_count || "-"}</strong>
                  <span>源画布</span>
                  <strong>{beautifyPlanPreview?.source?.canvas || "-"}</strong>
                  <span>身份来源</span>
                  <strong>{beautifyPlanPreview?.identity?.selected_source || "-"}</strong>
                  <span>清单 / 计划</span>
                  <strong>{beautifyPlanPreview ? `${beautifyPlanPreview.inventory?.slide_count || 0} 页 / 已生成` : "-"}</strong>
                  <span>风险</span>
                  <strong>{beautifyPlanPreview?.risks?.length ?? "-"}</strong>
                  <span>锁定说明</span>
                  <strong>页数、顺序、文字与数据不可更改</strong>
                </div>
                {canOpenBeautifyPlan(task) && (
                  <div className="button-row left">
                    <button className="primary-button" onClick={() => go({ name: "beautifyPlan", id: task.id })}>
                      <Palette size={16} />
                      <span>打开美化计划</span>
                    </button>
                  </div>
                )}
              </div>
            )}

            <div className="status-panel">
              <div className="section-title">
                <TerminalSquare size={17} />
                <span>Runtime</span>
              </div>
              <div className="kv-grid">
                <span>最近阶段</span>
                <strong className="mono">{latestRun?.phase || "-"}</strong>
                <span>运行状态</span>
                <strong className="mono">{latestRun?.status || "-"}</strong>
                <span>外部运行</span>
                <strong className="mono">{latestRun?.external_run_id || "-"}</strong>
                <span>Session</span>
                <strong className="mono">{latestRun?.external_session_id || "-"}</strong>
                <span>Workspace</span>
                <strong className="mono">{latestRun?.workspace_path || task.runtime_workspace_path || "-"}</strong>
              </div>
            </div>

            {task.status === "failed" && (
              <div className="status-panel failure-panel">
                <div className="section-title">
                  <XCircle size={17} />
                  <span>失败恢复</span>
                </div>
                <div className="error-block">
                  <strong>{task.failure_phase || "failed"}</strong>
                  <span>{task.error_message}</span>
                  {typeof failureMetadata.stderr_tail === "string" && failureMetadata.stderr_tail && (
                    <pre>{failureMetadata.stderr_tail}</pre>
                  )}
                  {task.failure_phase === "route_select.unsupported_workflow" && (
                    <span className="muted">该路线已识别，执行工作流将在后续阶段开放。</span>
                  )}
                  {failedPreflightChecks.length > 0 && (
                    <ul>
                      {failedPreflightChecks.map((check, index) => (
                        <li key={`${String(check.name || "check")}-${index}`}>
                          {String(check.name || "未命名检查")}: {String(check.message || "缺少必需能力")}
                        </li>
                      ))}
                    </ul>
                  )}
                  {retryGuidance && <span className="template-fill-recovery-note">{retryGuidance}</span>}
                </div>
                <div className="button-row left">
                  {retryOptions.map((option) => (
                    <button
                      className="secondary-button"
                      disabled={!!retrying}
                      key={option.phase}
                      onClick={() => void retry(option.phase)}
                    >
                      {retryPhaseIcon(option.phase, retrying === option.phase)}
                      <span>{option.label}</span>
                    </button>
                  ))}
                </div>
              </div>
            )}
          </div>

          <div className="log-surface">
            <div className="section-title">
              <LayoutList size={17} />
              <span>阶段时间线</span>
            </div>
            <div className="phase-run-list">
              {phaseRuns.map((run) => (
                <div className="phase-run-row" key={run.id}>
                  <span>{phaseLabel[run.phase] || run.phase}</span>
                  <span className="mono">{run.phase}</span>
                  <span className={`run-status ${run.status === "failed" ? "bad" : ""}`}>{run.status}</span>
                  <span className="mono">#{run.attempt}</span>
                  <span className="mono">{run.runner || "-"}</span>
                  <span className="muted">{formatTime(run.finished_at || run.updated_at)}</span>
                  <span className="mono">{run.error_message || run.runtime_run_id || "-"}</span>
                </div>
              ))}
              {phaseRuns.length === 0 && <InlineState icon={<Clock3 size={18} />} text="暂无阶段记录" />}
            </div>
          </div>

          <div className="log-surface">
            <div className="section-title">
              <Activity size={17} />
              <span>Runtime 运行尝试</span>
            </div>
            <div className="runtime-run-list">
              {runtimeRuns.slice(0, 8).map((run) => (
                <div className="runtime-run-row" key={run.id}>
                  <span className="mono">{run.phase}</span>
                  <span className={`run-status ${run.status === "failed" ? "bad" : ""}`}>{run.status}</span>
                  <span className="mono">{run.external_run_id || "-"}</span>
                  <span className="mono">{run.failure_phase || "-"}</span>
                  <span className="muted">{formatTime(run.finished_at || run.updated_at)}</span>
                </div>
              ))}
              {runtimeRuns.length === 0 && <InlineState icon={<Clock3 size={18} />} text="暂无 runtime run" />}
            </div>
          </div>

          <div className="log-surface">
            <div className="section-title">
              <TerminalSquare size={17} />
              <span>事件日志</span>
            </div>
            <div className="event-list">
              {events.map((event) => (
                <div className="event-row" key={event.id}>
                  <span className="event-seq">{event.seq}</span>
                  <span className="event-type">{event.type}</span>
                  <span className="event-status">{event.status}</span>
                  <span className="event-message">{event.message}</span>
                  <span className="muted">{formatTime(event.created_at)}</span>
                </div>
              ))}
            </div>
          </div>
        </>
      )}
    </section>
  );
}

function TemplateFillPlanPage({ taskId }: { taskId: string }) {
  const [requestScope] = useState(() => createTemplateFillRequestScope(
    taskId,
    () => taskRouteMatches(parseRoute(), "templateFill", taskId),
  ));
  const [task, setTask] = useState<Task | null>(null);
  const [preview, setPreview] = useState<TemplateFillPlanPreview | null>(null);
  const [planText, setPlanText] = useState("");
  const [savedPlanText, setSavedPlanText] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState<"regenerate" | "save" | "check" | "confirm" | "">("");
  const [error, setError] = useState("");

  function adoptPreview(next: TemplateFillPlanPreview) {
    const canonicalText = JSON.stringify(next.plan, null, 2);
    setPreview(next);
    setPlanText(canonicalText);
    setSavedPlanText(canonicalText);
  }

  const load = useCallback(async (generation: number) => {
    setLoading(true);
    setError("");
    try {
      const nextTask = await scopedTemplateFillRequest(requestScope, taskId, api.getTask, generation);
      if (!nextTask) {
        return;
      }
      if (nextTask.route !== "template-fill") {
        replaceRoute({ name: "task", id: nextTask.id });
        return;
      }
      setTask(nextTask);
      const nextPreview = await scopedTemplateFillRequest(requestScope, taskId, api.getTemplateFillPlan, generation);
      if (!nextPreview) {
        return;
      }
      adoptPreview(nextPreview);
    } catch (err) {
      if (requestScope.isGenerationCurrent(generation, taskId)) {
        setError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      if (requestScope.isGenerationCurrent(generation, taskId)) {
        setLoading(false);
      }
    }
  }, [requestScope, taskId]);

  useEffect(() => {
    return startTemplateFillRequestGeneration(requestScope, (generation) => void load(generation));
  }, [load, requestScope]);

  const dirty = planText !== savedPlanText;
  const checkErrorCount = preview ? numberFromSummary(preview.summary.check_error) : 0;
  const checkWarnCount = preview ? numberFromSummary(preview.summary.check_warn) : 0;
  const actionState = templateFillActionState({
    canEdit: !!preview?.can_edit,
    canConfirm: !!preview?.can_confirm,
    taskStatus: task?.status,
    busy: !!busy,
    dirty,
    checkErrorCount,
    checkWarningCount: checkWarnCount,
  });
  const slideRows = preview ? templateFillSlideRows(preview.plan) : [];
  const checkRows = preview ? templateFillCheckRows(preview.check_report) : [];
  const canRegenerate = task?.route === "template-fill"
    && (task?.status === "awaiting_template_fill_confirm" || task?.status === "failed");
  const recoveryGuidance = task ? retryGuidanceForFailure(task.failure_phase || "") : "";
  const sourcePptxName = preview
    ? templateFillText(preview.summary.source_pptx_name) !== "-"
      ? templateFillText(preview.summary.source_pptx_name)
      : templateFillBasename(preview.inputs?.source_pptx)
    : "-";

  async function regeneratePlan() {
    const requestGeneration = requestScope.currentGeneration();
    if (!canRegenerate || busy || !requestScope.isGenerationCurrent(requestGeneration, taskId)) {
      return;
    }
    setBusy("regenerate");
    setError("");
    try {
      const regenerated = await scopedTemplateFillRequest(
        requestScope,
        taskId,
        api.regenerateTemplateFillPlan,
        requestGeneration,
      );
      if (!regenerated) {
        return;
      }
      go({ name: "task", id: taskId });
    } catch (err) {
      if (requestScope.isGenerationCurrent(requestGeneration, taskId)) {
        setError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      if (requestScope.isGenerationCurrent(requestGeneration, taskId)) {
        setBusy("");
      }
    }
  }

  async function savePlan() {
    const requestGeneration = requestScope.currentGeneration();
    if (actionState.saveDisabled || !requestScope.isGenerationCurrent(requestGeneration, taskId)) {
      return;
    }
    setBusy("save");
    setError("");
    try {
      const parsed = JSON.parse(planText) as unknown;
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
        throw new Error("JSON 根节点必须是对象。");
      }
      const saved = await scopedTemplateFillRequest(
        requestScope,
        taskId,
        (scopedTaskId) => api.saveTemplateFillPlan(scopedTaskId, parsed as Record<string, unknown>),
        requestGeneration,
      );
      if (!saved) {
        return;
      }
      adoptPreview(saved);
      const canonical = await scopedTemplateFillRequest(
        requestScope,
        taskId,
        api.getTemplateFillPlan,
        requestGeneration,
      );
      if (!canonical) {
        return;
      }
      adoptPreview(canonical);
    } catch (err) {
      if (requestScope.isGenerationCurrent(requestGeneration, taskId)) {
        setError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      if (requestScope.isGenerationCurrent(requestGeneration, taskId)) {
        setBusy("");
      }
    }
  }

  async function checkPlan() {
    const requestGeneration = requestScope.currentGeneration();
    if (actionState.checkDisabled || !requestScope.isGenerationCurrent(requestGeneration, taskId)) {
      return;
    }
    setBusy("check");
    setError("");
    try {
      const checked = await scopedTemplateFillRequest(
        requestScope,
        taskId,
        api.checkTemplateFillPlan,
        requestGeneration,
      );
      if (!checked) {
        return;
      }
      setTask(checked);
      setPreview((current) => current ? {
        ...current,
        check_report: {},
        summary: {
          ...current.summary,
          check_ok: 0,
          check_warn: 0,
          check_error: 0,
        },
        can_confirm: false,
      } : current);
      const refreshed = await scopedTemplateFillRequest(
        requestScope,
        taskId,
        api.getTemplateFillPlan,
        requestGeneration,
      );
      if (!refreshed) {
        return;
      }
      adoptPreview(refreshed);
    } catch (err) {
      if (requestScope.isGenerationCurrent(requestGeneration, taskId)) {
        setError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      if (requestScope.isGenerationCurrent(requestGeneration, taskId)) {
        setBusy("");
      }
    }
  }

  async function confirmPlan() {
    const requestGeneration = requestScope.currentGeneration();
    if (actionState.confirmDisabled || !requestScope.isGenerationCurrent(requestGeneration, taskId)) {
      return;
    }
    setBusy("confirm");
    setError("");
    try {
      const confirmed = await scopedTemplateFillRequest(
        requestScope,
        taskId,
        api.confirmTemplateFillPlan,
        requestGeneration,
      );
      if (!confirmed) {
        return;
      }
      go({ name: "task", id: taskId });
    } catch (err) {
      if (requestScope.isGenerationCurrent(requestGeneration, taskId)) {
        setError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      if (requestScope.isGenerationCurrent(requestGeneration, taskId)) {
        setBusy("");
      }
    }
  }

  return (
    <section className="page template-fill-page">
      <PageHeader
        title="填充计划审查"
        subtitle={task?.title || taskId}
        actions={
          <>
            <button className="secondary-button" onClick={() => go({ name: "task", id: taskId })}>
              <ArrowLeft size={16} />
              <span>返回</span>
            </button>
            <button className="secondary-button" disabled={!canRegenerate || !!busy} onClick={() => void regeneratePlan()}>
              {busy === "regenerate" ? <Loader2 className="spin" size={16} /> : <RefreshCw size={16} />}
              <span>重新生成计划</span>
            </button>
            <button className="secondary-button" disabled={actionState.saveDisabled} onClick={() => void savePlan()}>
              {busy === "save" ? <Loader2 className="spin" size={16} /> : <FileText size={16} />}
              <span>保存 JSON</span>
            </button>
            <button
              className="secondary-button"
              disabled={actionState.checkDisabled}
              title={dirty ? actionState.hint : undefined}
              onClick={() => void checkPlan()}
            >
              {busy === "check" ? <Loader2 className="spin" size={16} /> : <ListChecks size={16} />}
              <span>检查计划</span>
            </button>
            <button
              className="primary-button"
              disabled={actionState.confirmDisabled}
              title={actionState.hint || undefined}
              onClick={() => void confirmPlan()}
            >
              {busy === "confirm" ? <Loader2 className="spin" size={17} /> : <Play size={17} />}
              <span>确认并导出{checkErrorCount > 0 ? `（${checkErrorCount} 个错误）` : ""}</span>
            </button>
          </>
        }
      />

      {actionState.hint && (
        <div className={checkErrorCount > 0 && !dirty ? "template-fill-action-hint bad" : "template-fill-action-hint"} role="status">
          {actionState.hint}
        </div>
      )}
      {error && <InlineState icon={<XCircle size={18} />} text={error} bad />}
      {recoveryGuidance && <div className="template-fill-action-hint bad">{recoveryGuidance}</div>}
      {loading && !preview && <InlineState icon={<Loader2 className="spin" size={18} />} text="加载填充计划" />}

      {preview && (
        <>
          <div className="template-fill-summary">
            <StatusPill status={task?.status || "awaiting_template_fill_confirm"} />
            <span className="summary-chip">
              计划状态
              <strong>{templateFillText(preview.summary.plan_status)}</strong>
            </span>
            <span className="summary-chip">
              计划页数
              <strong>{numberFromSummary(preview.summary.planned_slide_count)}</strong>
            </span>
            <span className="summary-chip">
              检查通过
              <strong>{numberFromSummary(preview.summary.check_ok)}</strong>
            </span>
            <span className="summary-chip">
              警告
              <strong>{checkWarnCount}</strong>
            </span>
            <span className="summary-chip">
              错误
              <strong>{checkErrorCount}</strong>
            </span>
            <span className="summary-chip source-pptx-chip">
              上传的 PPTX
              <strong>{sourcePptxName}</strong>
            </span>
            <p className="template-fill-source-note">
              模板填充由本任务上传的 PPTX 驱动，而不是创建任务时选择的目录模板。
            </p>
          </div>

          <div className="template-fill-layout">
            <section className="plan-preview-surface" aria-labelledby="template-fill-plan-preview-title">
              <div className="section-title" id="template-fill-plan-preview-title">
                <LayoutList size={17} />
                <span>逐页计划</span>
              </div>
              <div className="plan-slide-list">
                {slideRows.map((row) => (
                  <article className="plan-slide-row" key={row.index}>
                    <div className="plan-slide-heading">
                      <span>输出 {String(row.index).padStart(2, "0")}</span>
                      <strong>{row.purpose}</strong>
                    </div>
                    <dl className="plan-slide-details">
                      <div><dt>源页</dt><dd>{row.sourceSlide}</dd></div>
                      <div><dt>版式</dt><dd>{row.layoutPattern}</dd></div>
                      <div className="wide"><dt>适配原因</dt><dd>{row.whyFit}</dd></div>
                      <div className="wide"><dt>风险</dt><dd>{row.risk}</dd></div>
                      <div><dt>备注</dt><dd>{row.notes}</dd></div>
                      <div><dt>替换</dt><dd>{row.replacements}</dd></div>
                      <div><dt>表格编辑</dt><dd>{row.tableEdits}</dd></div>
                      <div><dt>图表编辑</dt><dd>{row.chartEdits}</dd></div>
                    </dl>
                  </article>
                ))}
                {slideRows.length === 0 && <InlineState icon={<Clock3 size={18} />} text="暂无计划页" />}
              </div>
            </section>

            <section className="plan-editor-surface" aria-labelledby="template-fill-json-title">
              <div className="section-title" id="template-fill-json-title">
                <FileText size={17} />
                <span>计划 JSON</span>
              </div>
              <div className="plan-file-meta">
                <span className="mono">{preview.plan_file.name}</span>
                <span>{formatBytes(preview.plan_file.size)}</span>
                <span>{formatTime(preview.plan_file.updated_at)}</span>
              </div>
              <span className="plan-file-path mono">{preview.plan_file.path}</span>
              <textarea
                className="plan-json-editor"
                aria-label="填充计划 JSON"
                readOnly={!preview.can_edit || !!busy}
                spellCheck={false}
                value={planText}
                onChange={(event) => setPlanText(event.target.value)}
              />
            </section>
          </div>

          <section className="check-report-surface" aria-labelledby="template-fill-report-title">
            <div className="section-title" id="template-fill-report-title">
              <ListChecks size={17} />
              <span>计划检查报告</span>
            </div>
            <div className="check-report-list">
              {checkRows.map((row, index) => (
                <div className={`check-report-row ${row.status === "ERROR" ? "bad" : "warn"}`} key={`${row.status}-${row.code}-${index}`}>
                  <strong>{row.status}</strong>
                  <span className="mono">{row.code}</span>
                  <span>计划页 {row.planSlide}</span>
                  <span>源页 {row.sourceSlide}</span>
                  <span>{row.message}</span>
                </div>
              ))}
              {checkRows.length === 0 && <InlineState icon={<CheckCircle2 size={18} />} text="暂无 ERROR / WARN" />}
            </div>
          </section>
        </>
      )}
    </section>
  );
}

function BeautifyPlanPage({ taskId }: { taskId: string }) {
  const [requestScope] = useState(() => createBeautifyPlanRequestScope(
    taskId,
    () => taskRouteMatches(parseRoute(), "beautifyPlan", taskId),
  ));
  const [task, setTask] = useState<Task | null>(null);
  const [preview, setPreview] = useState<BeautifyPlanPreview | null>(null);
  const [plan, setPlan] = useState<BeautifyPlan | null>(null);
  const [savedPlan, setSavedPlan] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState<"regenerate" | "save" | "check" | "confirm" | "">("");
  const [error, setError] = useState("");

  function adoptPreview(next: BeautifyPlanPreview) {
    const canonical = JSON.parse(JSON.stringify(next.plan)) as BeautifyPlan;
    setPreview(next);
    setPlan(canonical);
    setSavedPlan(JSON.stringify(canonical));
  }

  const load = useCallback(async (generation: number) => {
    setLoading(true);
    setError("");
    try {
      const nextTask = await scopedBeautifyPlanRequest(requestScope, taskId, api.getTask, generation);
      if (!nextTask) {
        return;
      }
      if (nextTask.route !== "beautify") {
        replaceRoute({ name: "task", id: nextTask.id });
        return;
      }
      setTask(nextTask);
      const nextPreview = await scopedBeautifyPlanRequest(requestScope, taskId, api.getBeautifyPlan, generation);
      if (!nextPreview || nextPreview.task_id !== taskId) {
        return;
      }
      adoptPreview(nextPreview);
    } catch (err) {
      if (requestScope.isGenerationCurrent(generation, taskId)) {
        setError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      if (requestScope.isGenerationCurrent(generation, taskId)) {
        setLoading(false);
      }
    }
  }, [requestScope, taskId]);

  useEffect(() => startTemplateFillRequestGeneration(requestScope, (generation) => void load(generation)), [load, requestScope]);

  const dirty = !!plan && JSON.stringify(plan) !== savedPlan;
  const errorCount = beautifyPlanErrorCount(preview);
  const actionState = beautifyPlanActionState({
    canEdit: !!preview?.can_edit,
    canConfirm: !!preview?.can_confirm,
    taskStatus: task?.status,
    busy: !!busy,
    dirty,
    errorCount,
  });
  const slideRows = beautifyPlanSlideRows(preview && plan ? { ...preview, plan } : preview);
  const canRegenerate = task?.route === "beautify"
    && (task.status === "awaiting_beautify_confirm" || task.status === "failed");

  function updateSlide(index: number, field: "layout_strategy" | "page_rhythm", value: string) {
    if (!preview?.can_edit || busy) {
      return;
    }
    setPlan((current) => current ? {
      ...current,
      slides: current.slides.map((slide, slideIndex) => slideIndex === index ? { ...slide, [field]: value } : slide),
    } : current);
  }

  function updateIdentitySource(value: string) {
    if (!preview?.can_edit || busy) {
      return;
    }
    setPlan((current) => current ? { ...current, identity: { ...current.identity, source: value } } : current);
  }

  function toggleRisk(riskID: string, accepted: boolean) {
    if (!preview?.can_edit || busy) {
      return;
    }
    setPlan((current) => {
      if (!current) {
        return current;
      }
      const acceptedRisks = new Set(current.accepted_risks || []);
      if (accepted) {
        acceptedRisks.add(riskID);
      } else {
        acceptedRisks.delete(riskID);
      }
      return { ...current, accepted_risks: [...acceptedRisks] };
    });
  }

  async function regeneratePlan() {
    const generation = requestScope.currentGeneration();
    if (!canRegenerate || busy || !requestScope.isGenerationCurrent(generation, taskId)) {
      return;
    }
    setBusy("regenerate");
    setError("");
    try {
      const next = await scopedBeautifyPlanRequest(requestScope, taskId, api.regenerateBeautifyPlan, generation);
      if (next) {
        go({ name: "task", id: taskId });
      }
    } catch (err) {
      if (requestScope.isGenerationCurrent(generation, taskId)) {
        setError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      if (requestScope.isGenerationCurrent(generation, taskId)) {
        setBusy("");
      }
    }
  }

  async function savePlan() {
    const generation = requestScope.currentGeneration();
    if (!plan || !preview || actionState.saveDisabled || !requestScope.isGenerationCurrent(generation, taskId)) {
      return;
    }
    setBusy("save");
    setError("");
    try {
      const saved = await scopedBeautifyPlanRequest(
        requestScope,
        taskId,
        (id) => api.saveBeautifyPlan(id, plan, preview.plan_sha256),
        generation,
      );
      if (!saved || saved.task_id !== taskId) {
        return;
      }
      adoptPreview(saved);
      const canonical = await scopedBeautifyPlanRequest(requestScope, taskId, api.getBeautifyPlan, generation);
      if (canonical && canonical.task_id === taskId) {
        adoptPreview(canonical);
      }
    } catch (err) {
      if (requestScope.isGenerationCurrent(generation, taskId)) {
        setError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      if (requestScope.isGenerationCurrent(generation, taskId)) {
        setBusy("");
      }
    }
  }

  async function checkPlan() {
    const generation = requestScope.currentGeneration();
    if (actionState.checkDisabled || !requestScope.isGenerationCurrent(generation, taskId)) {
      return;
    }
    setBusy("check");
    setError("");
    try {
      const checked = await scopedBeautifyPlanRequest(requestScope, taskId, api.checkBeautifyPlan, generation);
      if (!checked) {
        return;
      }
      setTask(checked);
      const canonical = await scopedBeautifyPlanRequest(requestScope, taskId, api.getBeautifyPlan, generation);
      if (canonical && canonical.task_id === taskId) {
        adoptPreview(canonical);
      }
    } catch (err) {
      if (requestScope.isGenerationCurrent(generation, taskId)) {
        setError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      if (requestScope.isGenerationCurrent(generation, taskId)) {
        setBusy("");
      }
    }
  }

  async function confirmPlan() {
    const generation = requestScope.currentGeneration();
    if (actionState.confirmDisabled || !requestScope.isGenerationCurrent(generation, taskId)) {
      return;
    }
    setBusy("confirm");
    setError("");
    try {
      const confirmed = await scopedBeautifyPlanRequest(requestScope, taskId, api.confirmBeautifyPlan, generation);
      if (confirmed) {
        go({ name: "task", id: taskId });
      }
    } catch (err) {
      if (requestScope.isGenerationCurrent(generation, taskId)) {
        setError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      if (requestScope.isGenerationCurrent(generation, taskId)) {
        setBusy("");
      }
    }
  }

  return (
    <section className="page beautify-plan-page">
      <PageHeader
        title="PPTX 美化计划"
        subtitle={task?.title || taskId}
        actions={
          <>
            <button className="secondary-button" onClick={() => go({ name: "task", id: taskId })}>
              <ArrowLeft size={16} /><span>返回</span>
            </button>
            <button className="secondary-button" disabled={!canRegenerate || !!busy} onClick={() => void regeneratePlan()}>
              {busy === "regenerate" ? <Loader2 className="spin" size={16} /> : <RefreshCw size={16} />}
              <span>重新生成计划</span>
            </button>
            <button className="secondary-button" disabled={actionState.saveDisabled} onClick={() => void savePlan()}>
              {busy === "save" ? <Loader2 className="spin" size={16} /> : <FileText size={16} />}
              <span>保存布局选择</span>
            </button>
            <button className="secondary-button" disabled={actionState.checkDisabled} onClick={() => void checkPlan()}>
              {busy === "check" ? <Loader2 className="spin" size={16} /> : <ListChecks size={16} />}
              <span>检查计划</span>
            </button>
            <button className="primary-button" disabled={actionState.confirmDisabled} onClick={() => void confirmPlan()}>
              {busy === "confirm" ? <Loader2 className="spin" size={17} /> : <Play size={17} />}
              <span>确认美化计划</span>
            </button>
          </>
        }
      />

      <div className="beautify-lock-notice" role="note">
        <strong>内容与数据已冻结</strong>
        <span>页数、页面顺序、可见文字、表格单元格和图表数据不可编辑。需要删页、合并、拆页或改写内容时，请转为主生成任务。</span>
      </div>
      {actionState.hint && <div className={`template-fill-action-hint${errorCount > 0 && !dirty ? " bad" : ""}`}>{actionState.hint}</div>}
      {error && <InlineState icon={<XCircle size={18} />} text={error} bad />}
      {loading && !preview && <InlineState icon={<Loader2 className="spin" size={18} />} text="加载美化计划" />}

      {preview && plan && (
        <>
          <div className="template-fill-summary beautify-plan-summary">
            <StatusPill status={task?.status || "awaiting_beautify_confirm"} />
            <span className="summary-chip"><span>源 PPTX</span><strong>{preview.source?.name || "-"}</strong></span>
            <span className="summary-chip"><span>页数锁定</span><strong>{preview.source?.slide_count || plan.slide_count}</strong></span>
            <span className="summary-chip"><span>画布</span><strong>{preview.source?.canvas || preview.identity?.canvas || "-"}</strong></span>
            <span className="summary-chip"><span>风险</span><strong>{preview.risks?.length || 0}</strong></span>
            <span className="summary-chip"><span>阻断</span><strong>{errorCount}</strong></span>
            <span className="summary-chip"><span>版本</span><strong>r{preview.revision || 1}</strong></span>
          </div>

          <div className="beautify-plan-layout">
            <section className="plan-preview-surface" aria-labelledby="beautify-plan-pages-title">
              <div className="section-title" id="beautify-plan-pages-title">
                <LayoutList size={17} /><span>1:1 逐页美化计划</span>
              </div>
              <div className="beautify-identity-control">
                <label htmlFor="beautify-identity-source">视觉身份来源</label>
                <select
                  id="beautify-identity-source"
                  disabled={!preview.can_edit || !!busy}
                  value={plan.identity.source}
                  onChange={(event) => updateIdentitySource(event.target.value)}
                >
                  {[plan.identity.source, "theme", "observed"].filter((value, index, values) => value && values.indexOf(value) === index).map((value) => (
                    <option value={value} key={value}>{value === "theme" ? "源主题（推荐）" : value === "observed" ? "观察到的颜色与字体" : value}</option>
                  ))}
                </select>
                <small>视觉覆盖会被审计并降低 paste-back guarantee，不影响冻结内容。</small>
              </div>
              <div className="plan-slide-list">
                {slideRows.map((slide, index) => (
                  <article className="plan-slide-row beautify-plan-slide" key={`${slide.source_slide}:${slide.output_page}`}>
                    <div className="plan-slide-heading">
                      <span>P{String(slide.source_slide).padStart(2, "0")} → P{String(slide.output_page).padStart(2, "0")}</span>
                      <strong>{slide.page_role || "内容页"}</strong>
                    </div>
                    <div className="beautify-plan-controls">
                      <label>
                        <span>布局策略</span>
                        <select
                          aria-label={`第 ${slide.source_slide} 页布局策略`}
                          disabled={!preview.can_edit || !!busy}
                          value={slide.layout_strategy}
                          onChange={(event) => updateSlide(index, "layout_strategy", event.target.value)}
                        >
                          {[
                            [slide.layout_strategy, slide.layout_strategy],
                            ["preserve_identity", "保留源身份，重排层级"],
                            ["clarify_hierarchy", "强化信息层级"],
                            ["increase_whitespace", "增加留白"],
                            ["rebalance_visuals", "平衡图文节奏"],
                          ].filter(([value], optionIndex, options) => value && options.findIndex(([candidate]) => candidate === value) === optionIndex).map(([value, label]) => (
                            <option value={value} key={value}>{label}</option>
                          ))}
                        </select>
                      </label>
                      <label>
                        <span>页面节奏</span>
                        <select
                          aria-label={`第 ${slide.source_slide} 页页面节奏`}
                          disabled={!preview.can_edit || !!busy}
                          value={slide.page_rhythm}
                          onChange={(event) => updateSlide(index, "page_rhythm", event.target.value)}
                        >
                          {[
                            [slide.page_rhythm, slide.page_rhythm],
                            ["anchor", "锚点页"],
                            ["flow", "叙事流"],
                            ["breathing", "留白节奏"],
                            ["dense", "高密度"],
                          ].filter(([value], optionIndex, options) => value && options.findIndex(([candidate]) => candidate === value) === optionIndex).map(([value, label]) => (
                            <option value={value} key={value}>{label}</option>
                          ))}
                        </select>
                      </label>
                    </div>
                    <dl className="plan-slide-details beautify-frozen-details">
                      <div><dt>文字块</dt><dd>{slide.inventory?.text_count ?? slide.text_block_ids.length}</dd></div>
                      <div><dt>图片</dt><dd>{slide.inventory?.image_count ?? slide.image_ids.length}</dd></div>
                      <div><dt>表格</dt><dd>{slide.inventory?.table_count ?? slide.table_ids.length}</dd></div>
                      <div><dt>图表</dt><dd>{slide.inventory?.chart_count ?? slide.chart_ids.length}</dd></div>
                      <div><dt>忽略</dt><dd>{slide.ignored.length}</dd></div>
                      <div><dt>不支持</dt><dd>{slide.unsupported.length}</dd></div>
                      <div className="wide"><dt>冻结项</dt><dd>文字/图片/表格/图表 ID 只读绑定，不可跨页、删除或改写</dd></div>
                    </dl>
                  </article>
                ))}
                {slideRows.length === 0 && <InlineState icon={<Clock3 size={18} />} text="暂无美化计划页" />}
              </div>
            </section>

            <section className="plan-editor-surface beautify-risk-surface" aria-labelledby="beautify-risk-title">
              <div className="section-title" id="beautify-risk-title">
                <ListChecks size={17} /><span>复杂对象与风险决策</span>
              </div>
              <div className="beautify-risk-list">
                {(preview.risks || []).map((risk) => {
                  const accepted = plan.accepted_risks.includes(risk.id);
                  return (
                    <label className={`beautify-risk-row severity-${risk.severity}`} key={risk.id}>
                      <input
                        type="checkbox"
                        disabled={!preview.can_edit || !!busy}
                        checked={accepted}
                        onChange={(event) => toggleRisk(risk.id, event.target.checked)}
                      />
                      <span>
                        <strong>P{String(risk.source_slide).padStart(2, "0")} · {risk.code || risk.object_type}</strong>
                        <small>{risk.message}</small>
                      </span>
                    </label>
                  );
                })}
                {(preview.risks || []).length === 0 && <InlineState icon={<CheckCircle2 size={18} />} text="没有待确认风险" />}
              </div>
              <div className="beautify-readonly-summary">
                <strong>只读决策摘要</strong>
                <span>全局忽略 {plan.global_ignored.length}</span>
                <span>已接受风险 {plan.accepted_risks.length}</span>
                <span className="mono">Plan {preview.plan_sha256 ? preview.plan_sha256.slice(0, 12) : "-"}</span>
              </div>
            </section>
          </div>

          <section className="check-report-surface" aria-labelledby="beautify-plan-findings-title">
            <div className="section-title" id="beautify-plan-findings-title">
              <ListChecks size={17} /><span>计划检查结果</span>
            </div>
            <div className="check-report-list">
              {(preview.findings || []).map((finding) => (
                <div
                  className={`check-report-row ${["blocking", "error"].includes(finding.severity) ? "bad" : "warn"}`}
                  key={finding.id}
                >
                  <strong>{finding.severity.toUpperCase()}</strong>
                  <span className="mono">{finding.code}</span>
                  <span>源页 {finding.source_slide || "-"}</span>
                  <span>计划</span>
                  <span>{finding.message}</span>
                </div>
              ))}
              {(preview.findings || []).length === 0 && <InlineState icon={<CheckCircle2 size={18} />} text="暂无阻断或警告" />}
            </div>
          </section>
        </>
      )}
    </section>
  );
}

function ConfirmPage({ taskId }: { taskId: string }) {
  const [task, setTask] = useState<Task | null>(null);
  const [confirmations, setConfirmations] = useState<Confirmation[]>([]);
  const [values, setValues] = useState<Record<string, unknown>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      const [nextTask, nextConfirmations] = await Promise.all([api.getTask(taskId), api.listConfirmations(taskId)]);
      setTask(nextTask);
      setConfirmations(nextConfirmations);
      setValues(defaultConfirmationValues(nextConfirmations));
      setError("");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [taskId]);

  useEffect(() => {
    void load();
  }, [load]);

  async function submit() {
    setBusy(true);
    setError("");
    try {
      const nextTask = await api.submitConfirmations(taskId, confirmationSubmissionValues(task, values));
      setTask(nextTask);
      if (nextTask.status === "awaiting_realization_confirm") {
        const nextConfirmations = await api.listConfirmations(taskId);
        setConfirmations(nextConfirmations);
        setValues(defaultConfirmationValues(nextConfirmations));
      } else if (nextTask.status === "awaiting_spec_confirm") {
        go({ name: "spec", id: taskId });
      } else if (nextTask.status === "completed") {
        go(completedTaskRoute(nextTask.id, nextTask.route));
      } else {
        go({ name: "task", id: taskId });
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  const tierTitle = task?.status === "awaiting_realization_confirm" ? "确认表现方式" : "确认生成目标";
  const tierNote =
    task?.status === "awaiting_realization_confirm"
      ? task.route === "beautify"
        ? "源页数与顺序已锁定；确认配色、字体、源图片策略和生成模式。"
        : "根据你已确认的目标锚点，确认页数、色彩、字体、图片和生成模式。"
      : "先确认画布、受众、叙事模式和视觉方向；下一步会据此重新推导表现层推荐。";
  const submitLabel = task?.status === "awaiting_realization_confirm" ? "确认并生成" : "下一步";

  return (
    <section className="page">
      <PageHeader
        title={tierTitle}
        subtitle={task?.title || taskId}
        actions={
          <button className="secondary-button" onClick={() => go({ name: "task", id: taskId })}>
            <ArrowLeft size={16} />
            <span>返回</span>
          </button>
        }
      />

      {error && <InlineState icon={<XCircle size={18} />} text={error} bad />}
      <div className="confirm-intro">
        <StatusPill status={task?.status || "awaiting_anchor_confirm"} />
        <span>{tierNote}</span>
      </div>
      <div className="confirm-grid">
        {confirmations.map((confirmation) => (
          <ConfirmationField
            key={confirmation.id}
            confirmation={confirmation}
            value={values[confirmation.key]}
            locked={task?.route === "beautify" && confirmation.key === "page_count"}
            onChange={(value) => setValues((current) => ({ ...current, [confirmation.key]: value }))}
          />
        ))}
      </div>
      <div className="submit-strip">
        <StatusPill status={task?.status || "awaiting_anchor_confirm"} />
        <button className="primary-button" disabled={busy || confirmations.length === 0} onClick={() => void submit()}>
          {busy ? <Loader2 className="spin" size={17} /> : <CheckCircle2 size={17} />}
          <span>{busy ? "提交中" : submitLabel}</span>
        </button>
      </div>
    </section>
  );
}

function SpecPreviewPage({ taskId }: { taskId: string }) {
  const [task, setTask] = useState<Task | null>(null);
  const [spec, setSpec] = useState<SpecPreview | null>(null);
  const [busy, setBusy] = useState<"svg_execute" | "spec_generate" | "">("");
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      const nextTask = await api.getTask(taskId);
      setTask(nextTask);
      if (
        nextTask.status === "awaiting_spec_confirm" ||
        nextTask.status === "svg_generating" ||
        nextTask.status === "quality_checking" ||
        nextTask.status === "exporting" ||
        nextTask.status === "publishing" ||
        nextTask.status === "completed"
      ) {
        setSpec(await api.getSpecPreview(taskId));
      }
      setError("");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [taskId]);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 2500);
    return () => window.clearInterval(timer);
  }, [load]);

  async function continueToSVG() {
    setBusy("svg_execute");
    setError("");
    try {
      await api.continueTask(taskId, "svg_execute");
      go({ name: "task", id: taskId });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy("");
    }
  }

  async function regenerateSpec() {
    setBusy("spec_generate");
    setError("");
    try {
      await api.continueTask(taskId, "spec_generate");
      go({ name: "task", id: taskId });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy("");
    }
  }

  const canConfirm = task?.status === "awaiting_spec_confirm";
  const summaryRows = spec ? specSummaryRows(spec.summary) : [];

  return (
    <section className="page spec-page">
      <PageHeader
        title="规格审查"
        subtitle={task?.title || taskId}
        actions={
          <>
            <button className="secondary-button" onClick={() => go({ name: "task", id: taskId })}>
              <ArrowLeft size={16} />
              <span>返回</span>
            </button>
            <button className="secondary-button" disabled={!canConfirm || !!busy} onClick={() => void regenerateSpec()}>
              {busy === "spec_generate" ? <Loader2 className="spin" size={16} /> : <RefreshCw size={16} />}
              <span>重新生成规格</span>
            </button>
            <button className="primary-button" disabled={!canConfirm || !!busy} onClick={() => void continueToSVG()}>
              {busy === "svg_execute" ? <Loader2 className="spin" size={17} /> : <Play size={17} />}
              <span>确认并生成 SVG</span>
            </button>
          </>
        }
      />

      {error && <InlineState icon={<XCircle size={18} />} text={error} bad />}
      {!spec && !error && <InlineState icon={<Loader2 className="spin" size={18} />} text="加载中" />}
      {spec && (
        <>
          <div className="spec-summary">
            <StatusPill status={task?.status || "awaiting_spec_confirm"} />
            {summaryRows.map((row) => (
              <span className="summary-chip" key={row.label}>
                {row.label}
                <strong>{row.value}</strong>
              </span>
            ))}
          </div>

          <div className="spec-layout">
            <SpecFilePanel file={spec.design_spec} title="Design Spec" />
            <SpecFilePanel file={spec.spec_lock} title="Spec Lock" />
          </div>
        </>
      )}
    </section>
  );
}

function SpecFilePanel({ file, title }: { file: SpecPreview["design_spec"]; title: string }) {
  return (
    <div className="spec-panel">
      <div className="section-title">
        <FileText size={17} />
        <span>{title}</span>
      </div>
      <div className="spec-file-meta">
        <span className="mono">{file.name}</span>
        <span>{formatBytes(file.size)}</span>
        <span>{formatTime(file.updated_at)}</span>
      </div>
      <pre>{file.content}</pre>
    </div>
  );
}

const activeEditSessionStatuses = new Set(["draft", "queued", "materializing", "applying_direct_edits", "applying_annotations", "svg_validating", "quality_checking", "exporting", "pptx_validating", "publishing"]);

function emptyManualEditDraft(): ManualEditDraft {
	return { schema: "slidesmith.manual_edit_draft.v1", pages: [], annotations: [] };
}

function editSessionStatusText(status?: string) {
	return ({
		draft: "草稿", queued: "已排队", materializing: "准备隔离副本", applying_direct_edits: "应用直接修改",
		applying_annotations: "应用 AI 批注", svg_validating: "校验 SVG", quality_checking: "质量门禁",
		exporting: "重新导出", pptx_validating: "校验 PPTX", publishing: "发布新版本", published: "已发布",
		failed: "失败", stale: "基线已过期", discarded: "已放弃",
	} as Record<string, string>)[status || ""] || status || "未开始";
}

function previewIframeDocument(snapshot: ManualEditPageSnapshot) {
	const controller = `<script>(()=>{const meta=${JSON.stringify({ session_id: snapshot.session_id, page_id: snapshot.page_id, revision: snapshot.revision })};const nodes=()=>Array.from(document.querySelectorAll('[data-editor-selectable="true"]'));document.addEventListener('click',event=>{const node=event.target.closest('[data-editor-selectable="true"]');if(!node)return;event.preventDefault();nodes().forEach(item=>item.removeAttribute('data-editor-selected'));node.setAttribute('data-editor-selected','true');parent.postMessage({schema:'slidesmith.editor_message.v1',type:'select',...meta,element_id:node.getAttribute('data-editor-id'),element_fingerprint:node.getAttribute('data-editor-fingerprint')},'*')});addEventListener('message',event=>{const message=event.data||{};if(message.schema!=='slidesmith.editor_message.v1'||message.type!=='apply_preview'||message.session_id!==meta.session_id||message.page_id!==meta.page_id||message.revision!==meta.revision)return;const node=nodes().find(item=>item.getAttribute('data-editor-id')===message.element_id);if(!node)return;if(message.operation==='set_text')node.textContent=String(message.value.text??'');else if(message.operation==='translate'){const dx=message.value.dx===undefined?Number(node.dataset.editorDx||0):Number(message.value.dx);const dy=message.value.dy===undefined?Number(node.dataset.editorDy||0):Number(message.value.dy);node.dataset.editorDx=String(dx);node.dataset.editorDy=String(dy);node.setAttribute('transform','translate('+dx+' '+dy+')')}else{const attrs={set_fill:'fill',set_stroke:'stroke',set_opacity:'opacity',set_font_size:'font-size',set_font_family:'font-family',set_font_weight:'font-weight',set_text_anchor:'text-anchor'};const attr=attrs[message.operation];if(attr)node.setAttribute(attr,String(message.value[message.operation.replace('set_','')]??''));}});})();</script>`;
	return `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data:; style-src 'unsafe-inline'; script-src 'unsafe-inline'"><style>html,body{margin:0;width:100%;height:100%;overflow:hidden;background:#fff}svg{display:block;width:100%;height:100%}[data-editor-selectable="true"]{cursor:pointer}[data-editor-selected="true"]{outline:3px solid #d8762f;outline-offset:3px}</style></head><body>${snapshot.svg}${controller}</body></html>`;
}

let editClientIDSequence = 0;

function newEditClientID() {
	if (typeof globalThis.crypto?.randomUUID === "function") return globalThis.crypto.randomUUID();
	const random = new Uint8Array(16);
	if (typeof globalThis.crypto?.getRandomValues === "function") globalThis.crypto.getRandomValues(random);
	else for (let index = 0; index < random.length; index += 1) random[index] = Math.floor(Math.random() * 256);
	random[6] = (random[6] & 0x0f) | 0x40;
	random[8] = (random[8] & 0x3f) | 0x80;
	const hex = Array.from(random, (value) => value.toString(16).padStart(2, "0")).join("");
	editClientIDSequence += 1;
	return `${hex.slice(0,8)}-${hex.slice(8,12)}-${hex.slice(12,16)}-${hex.slice(16,20)}-${hex.slice(20)}-${editClientIDSequence.toString(16)}`;
}

function manualEditOperationMatchesBase(type: ManualEditOperation["type"], value: ManualEditOperation["value"], element: ManualEditSnapshotElement) {
	switch (type) {
		case "set_text": return String(value.text ?? "") === (element.text || "");
		case "set_fill": return String(value.fill ?? "").trim().toLowerCase() === (element.attributes.fill || "#000000").trim().toLowerCase();
		case "set_stroke": return String(value.stroke ?? "").trim().toLowerCase() === (element.attributes.stroke || "none").trim().toLowerCase();
		case "set_opacity": return Number(value.opacity) === Number(element.attributes.opacity || "1");
		case "set_font_size": return Number(value.font_size) === Number(element.attributes["font-size"] || "32");
		case "set_font_family": return String(value.font_family ?? "").trim() === (element.attributes["font-family"] || "Arial").trim();
		case "set_font_weight": return String(value.font_weight ?? "").trim() === (element.attributes["font-weight"] || "normal").trim();
		case "set_text_anchor": return String(value.text_anchor ?? "").trim() === (element.attributes["text-anchor"] || "start").trim();
		default: return false;
	}
}

function PreviewPage({ taskId }: { taskId: string }) {
	const [state, setState] = useState(() => createPreviewPageState(taskId));
	const visibleState = previewPageStateForTask(state, taskId);
	const [selectedPageId, setSelectedPageId] = useState("");
	const [snapshot, setSnapshot] = useState<ManualEditPageSnapshot | null>(null);
	const [selectedElementId, setSelectedElementId] = useState("");
	const [draft, setDraft] = useState<ManualEditDraft>(emptyManualEditDraft);
	const [dirty, setDirty] = useState(false);
	const [conflict, setConflict] = useState(false);
	const [busy, setBusy] = useState("");
	const [annotationText, setAnnotationText] = useState("");
	const [runs, setRuns] = useState<EditRun[]>([]);
	const iframeRef = useRef<HTMLIFrameElement>(null);

	const setError = useCallback((error: string) => setState((current) => current.taskId === taskId ? { ...current, error } : current), [taskId]);
	const setActiveSession = useCallback((session: EditSession | null) => {
		setState((current) => current.taskId === taskId ? { ...current, activeSession: session } : current);
		setDraft(session ? parseJSON<ManualEditDraft>(session.draft, emptyManualEditDraft()) : emptyManualEditDraft());
		setDirty(false);
		setConflict(false);
	}, [taskId]);

	useEffect(() => {
		let active = true;
		async function load() {
			try {
				const result = await loadPreviewPageData(
					taskId, api.getTask, api.listArtifacts,
					() => active && taskRouteMatches(parseRoute(), "preview", taskId), replaceRoute,
				);
				if (!result) return;
				const { task: nextTask, artifacts: legacyArtifacts } = result;
				const [versions, sessions] = await Promise.all([api.listArtifactVersions(taskId), api.listEditSessions(taskId)]);
				if (!active || !taskRouteMatches(parseRoute(), "preview", taskId)) return;
				const selectedVersion = versions.find((version) => version.is_latest)?.version || versions[0]?.version || "";
				const [nextArtifacts, bundle] = selectedVersion
					? await Promise.all([api.listArtifactsByVersion(taskId, selectedVersion), api.getSVGBundleByVersion(taskId, selectedVersion)])
					: [legacyArtifacts, emptySVGBundle(taskId)];
				if (!active || !taskRouteMatches(parseRoute(), "preview", taskId)) return;
				const activeSession = sessions.find((session) => activeEditSessionStatuses.has(session.status) && session.base_publish_version === selectedVersion)
					|| sessions.find((session) => session.status === "failed" && session.base_publish_version === selectedVersion)
					|| null;
				setState({ taskId, task: nextTask, artifacts: nextArtifacts, versions, selectedVersion, bundle, sessions, activeSession, selectedId: "", error: "" });
				setSelectedPageId(bundle.pages[0]?.page_id || "");
				setDraft(activeSession ? parseJSON<ManualEditDraft>(activeSession.draft, emptyManualEditDraft()) : emptyManualEditDraft());
			} catch (err) {
				if (active && taskRouteMatches(parseRoute(), "preview", taskId)) {
					setState({ ...createPreviewPageState(taskId), error: err instanceof Error ? err.message : String(err) });
				}
			}
		}
		void load();
		return () => { active = false; };
	}, [taskId]);

	useEffect(() => {
		const session = visibleState.activeSession;
		if (!session || !selectedPageId || visibleState.selectedVersion !== session.base_publish_version) {
			setSnapshot(null);
			setSelectedElementId("");
			return;
		}
		let active = true;
		api.getEditSessionPage(taskId, session.id, selectedPageId).then((page) => {
			if (active) { setSnapshot(page); setSelectedElementId(""); }
		}).catch((err) => { if (active) { setSnapshot(null); setError(err instanceof Error ? err.message : String(err)); } });
		return () => { active = false; };
	}, [taskId, selectedPageId, visibleState.activeSession?.id, visibleState.activeSession?.revision, visibleState.selectedVersion, setError]);

	useEffect(() => {
		function onMessage(event: MessageEvent) {
			const message = event.data as Record<string, unknown>;
			if (event.source !== iframeRef.current?.contentWindow || message?.schema !== "slidesmith.editor_message.v1" || message.type !== "select") return;
			if (!snapshot || message.session_id !== snapshot.session_id || message.page_id !== snapshot.page_id || message.revision !== snapshot.revision) return;
			if (typeof message.element_id === "string" && snapshot.elements.some((element) => element.element_id === message.element_id && element.element_fingerprint === message.element_fingerprint)) setSelectedElementId(message.element_id);
		}
		window.addEventListener("message", onMessage);
		return () => window.removeEventListener("message", onMessage);
	}, [snapshot]);

	useEffect(() => {
		if (!dirty) return;
		const guard = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
		window.addEventListener("beforeunload", guard);
		return () => window.removeEventListener("beforeunload", guard);
	}, [dirty]);

	useEffect(() => {
		const session = visibleState.activeSession;
		if (!session || session.status === "draft" || !activeEditSessionStatuses.has(session.status)) return;
		let active = true;
		const poll = async () => {
			try {
				const [next, nextRuns] = await Promise.all([api.getEditSession(taskId, session.id), api.listEditRuns(taskId, session.id)]);
				if (!active) return;
				setRuns(nextRuns);
				setActiveSession(next);
				if (next.status === "published" && next.result_publish_version) await selectVersion(next.result_publish_version);
			} catch (err) { if (active) setError(err instanceof Error ? err.message : String(err)); }
		};
		const timer = window.setInterval(() => void poll(), 2000);
		void poll();
		return () => { active = false; window.clearInterval(timer); };
	}, [taskId, visibleState.activeSession?.id, visibleState.activeSession?.status]);

	async function selectVersion(version: string) {
		setBusy("version"); setError(""); setSnapshot(null);
		try {
			const versions = await api.listArtifactVersions(taskId);
			const selectedVersion = versions.some((candidate) => candidate.version === version)
				? version
				: versions.find((candidate) => candidate.is_latest)?.version || versions[0]?.version || "";
			const [artifacts, bundle, sessions] = selectedVersion
				? await Promise.all([api.listArtifactsByVersion(taskId, selectedVersion), api.getSVGBundleByVersion(taskId, selectedVersion), api.listEditSessions(taskId)])
				: [[], emptySVGBundle(taskId), await api.listEditSessions(taskId)];
			const activeSession = sessions.find((session) => activeEditSessionStatuses.has(session.status) && session.base_publish_version === selectedVersion) || null;
			setState((current) => current.taskId === taskId ? { ...current, artifacts, bundle, sessions, versions, selectedVersion, activeSession } : current);
			setSelectedPageId(bundle.pages[0]?.page_id || "");
			setActiveSession(activeSession);
		} catch (err) { setError(err instanceof Error ? err.message : String(err)); }
		finally { setBusy(""); }
	}

	async function startEditing() {
		if (!visibleState.selectedVersion) return;
		setBusy("create"); setError("");
		try { const session = await api.createEditSession(taskId, visibleState.selectedVersion); setActiveSession(session); }
		catch (err) { setError(err instanceof Error ? err.message : String(err)); }
		finally { setBusy(""); }
	}

	function stageOperation(type: ManualEditOperation["type"], value: ManualEditOperation["value"]) {
		const session = visibleState.activeSession;
		const element = snapshot?.elements.find((candidate) => candidate.element_id === selectedElementId);
		if (!session || session.status !== "draft" || !element || !snapshot) return;
		const target = { element_id: element.element_id, source_id: element.source_id, tag: element.tag, element_fingerprint: element.element_fingerprint };
		const next = structuredClone(draft);
		let page = next.pages.find((candidate) => candidate.page_id === snapshot.page_id);
		let existing = page?.operations.find((operation) => operation.type === type && operation.target.element_id === element.element_id);
		let nextValue = value;
		let remove = manualEditOperationMatchesBase(type, value, element);
		if (type === "translate") {
			nextValue = { dx:0, dy:0, ...(existing?.value || {}), ...value };
			remove = Number(nextValue.dx) === 0 && Number(nextValue.dy) === 0;
		}
		if (remove) {
			if (!page || !existing) return;
			page.operations = page.operations.filter((operation) => operation !== existing);
			if (page.operations.length === 0) next.pages = next.pages.filter((candidate) => candidate !== page);
		} else {
			if (!page) { page = { page_id: snapshot.page_id, base_svg_sha256: snapshot.base_svg_sha256, operations: [] }; next.pages.push(page); }
			existing = page.operations.find((operation) => operation.type === type && operation.target.element_id === element.element_id);
			if (existing) {
				if (JSON.stringify(existing.value) === JSON.stringify(nextValue)) return;
				existing.value = nextValue;
			} else page.operations.push({ operation_id: newEditClientID(), type, target, value: nextValue });
		}
		next.client_updated_at = new Date().toISOString();
		setDraft(next);
		setDirty(true); setConflict(false);
		iframeRef.current?.contentWindow?.postMessage({ schema:"slidesmith.editor_message.v1", type:"apply_preview", session_id:session.id, page_id:snapshot.page_id, revision:snapshot.revision, element_id:element.element_id, operation:type, value }, "*");
	}

	async function saveDraft() {
		const session = visibleState.activeSession;
		if (!session || session.status !== "draft" || conflict) return session;
		setBusy("save"); setError("");
		try {
			const updated = await api.saveEditSessionDraft(taskId, session.id, session.revision, draft);
			setActiveSession(updated); setDirty(false); return updated;
		} catch (err) {
			if (err instanceof APIError && err.status === 409) setConflict(true);
			setError(err instanceof Error ? err.message : String(err)); return null;
		} finally { setBusy(""); }
	}

	async function applyDraft() {
		let session = visibleState.activeSession;
		if (!session || conflict) return;
		if (dirty) session = await saveDraft();
		if (!session) return;
		setBusy("apply"); setError("");
		try { setActiveSession(await api.applyEditSession(taskId, session.id, session.revision, session.draft_sha256)); }
		catch (err) { setError(err instanceof Error ? err.message : String(err)); }
		finally { setBusy(""); }
	}

	function addAnnotation() {
		const instruction = annotationText.trim();
		if (!instruction || !selectedPageId) return;
		const element = snapshot?.elements.find((candidate) => candidate.element_id === selectedElementId);
		setDraft((current) => ({ ...current, annotations: [...current.annotations, {
			annotation_id: newEditClientID(), scope: element ? "element" : "page", page_id: selectedPageId,
			...(element ? { target: { element_id:element.element_id, source_id:element.source_id, tag:element.tag, element_fingerprint:element.element_fingerprint } } : {}), instruction, status:"pending",
		}], client_updated_at:new Date().toISOString() }));
		setAnnotationText(""); setDirty(true);
	}

	const selectedElement: ManualEditSnapshotElement | undefined = snapshot?.elements.find((element) => element.element_id === selectedElementId);
	const selectedVersion = visibleState.versions.find((version) => version.version === visibleState.selectedVersion);
	const editCapabilities = parseJSON<{ annotation?: boolean }>(visibleState.activeSession?.capability_snapshot || "{}", {});
	const isLatest = !!selectedVersion?.is_latest;
	const canCreateSession = visibleState.task?.status === "completed" && visibleState.task.route === "main" && visibleState.task.runner_profile === "full-ppt-master" && isLatest && !visibleState.activeSession;
	const editable = visibleState.activeSession?.status === "draft" && isLatest && !!snapshot;
	const svgArtifacts = visibleState.artifacts.filter((artifact) => artifact.kind === "svg_final");
	const pageIndex = Math.max(0, visibleState.bundle.pages.findIndex((page) => page.page_id === selectedPageId));
	const fallbackSVG = svgArtifacts[pageIndex] || svgArtifacts[0];
	const pptx = visibleState.artifacts.find((artifact) => artifact.kind === "pptx");

	return (
		<section className="page preview-page live-preview-page">
			<PageHeader title="Live Preview 与人工修改" subtitle={visibleState.task?.title || taskId} actions={<>
				<button className="secondary-button" onClick={() => { if (!dirty || window.confirm("有未保存修改，确定离开？")) go({ name:"task", id:taskId }); }}><ArrowLeft size={16}/><span>返回</span></button>
				<select className="version-selector" value={visibleState.selectedVersion} disabled={busy === "version"} onChange={(event) => void selectVersion(event.target.value)} aria-label="发布版本">
					{visibleState.versions.map((version) => <option key={version.version} value={version.version}>{version.is_latest ? "最新 · " : ""}{version.version} · {version.source === "manual_edit" ? "人工修改" : "生成"}</option>)}
				</select>
				{canCreateSession && <button className="secondary-button" disabled={!!busy} onClick={() => void startEditing()}><Palette size={16}/><span>开始编辑</span></button>}
				<a className={pptx ? "primary-button" : "primary-button disabled"} href={pptx ? api.versionPPTXDownloadUrl(taskId, visibleState.selectedVersion) : undefined}><Download size={17}/><span>下载此版本</span></a>
			</>}/>
			{visibleState.error && <InlineState icon={<XCircle size={18}/>} text={visibleState.error} bad/>}
			{conflict && <InlineState icon={<XCircle size={18}/>} text="草稿已被其他窗口更新；已停止保存，请刷新或复制本地内容后重试。" bad/>}
			<div className="live-preview-meta">
				<span>{selectedVersion?.source === "manual_edit" ? "人工修改版本" : "生成版本"}</span><span className="mono">{visibleState.selectedVersion || "-"}</span>
				<span>父版本 {selectedVersion?.parent_version || "-"}</span><span>质量 {visibleState.bundle.passed ? "通过" : "只读回退"}</span>
				<span>会话 {editSessionStatusText(visibleState.activeSession?.status)}</span>{dirty && <strong>有未保存修改</strong>}
			</div>
			<div className="preview-layout live-preview-layout">
				<div className="slide-rail">
					{visibleState.bundle.pages.map((page) => <button className={page.page_id === selectedPageId ? "slide-thumb active" : "slide-thumb"} key={page.page_id} onClick={() => setSelectedPageId(page.page_id)}>
						<span>{String(page.page).padStart(2,"0")}</span><small>{page.filename}</small>
						{draft.pages.some((candidate) => candidate.page_id === page.page_id && candidate.operations.length) && <b>已改</b>}
						{draft.annotations.some((annotation) => annotation.page_id === page.page_id) && <b>AI</b>}
					</button>)}
					{visibleState.bundle.pages.length === 0 && <InlineState icon={<Clock3 size={18}/>} text="暂无 SVG"/>}
				</div>
				<div className="svg-stage live-svg-stage">
					{snapshot && visibleState.activeSession && visibleState.selectedVersion === visibleState.activeSession.base_publish_version ? <iframe ref={iframeRef} title={`${snapshot.page_id} editor`} sandbox="allow-scripts" srcDoc={previewIframeDocument(snapshot)}/> : fallbackSVG ? <img alt={fallbackSVG.name} src={api.artifactContentUrl(taskId, fallbackSVG.id)}/> : <InlineState icon={<Clock3 size={18}/>} text="-"/>}
					{!editable && fallbackSVG && <span className="readonly-badge">只读预览</span>}
				</div>
				<aside className="edit-inspector">
					<div className="section-title"><Layers size={16}/><span>Inspect / 修改</span></div>
					{selectedElement ? <>
						<div className="element-identity"><strong>{selectedElement.tag} · {selectedElement.element_id}</strong><small className="mono">{selectedElement.element_fingerprint.slice(0,24)}</small></div>
						{(selectedElement.tag === "text" || selectedElement.tag === "tspan") && <label>文字<textarea disabled={!editable} defaultValue={selectedElement.text || ""} onBlur={(event) => stageOperation("set_text",{text:event.target.value})}/></label>}
						<div className="two-field"><label>水平移动<input type="number" disabled={!editable} defaultValue={0} onBlur={(event)=>stageOperation("translate",{dx:Number(event.target.value)})}/></label><label>垂直移动<input type="number" disabled={!editable} defaultValue={0} onBlur={(event)=>stageOperation("translate",{dy:Number(event.target.value)})}/></label></div>
						<label>填充色<input disabled={!editable} defaultValue={selectedElement.attributes.fill || "#000000"} onBlur={(event)=>stageOperation("set_fill",{fill:event.target.value})}/></label>
						<label>透明度<input type="number" min="0" max="1" step="0.05" disabled={!editable} defaultValue={selectedElement.attributes.opacity || "1"} onBlur={(event)=>stageOperation("set_opacity",{opacity:Number(event.target.value)})}/></label>
						{(selectedElement.tag === "text" || selectedElement.tag === "tspan") && <><label>字号<input type="number" disabled={!editable} defaultValue={selectedElement.attributes["font-size"] || "32"} onBlur={(event)=>stageOperation("set_font_size",{font_size:Number(event.target.value)})}/></label><label>字体<input disabled={!editable} defaultValue={selectedElement.attributes["font-family"] || "Arial"} onBlur={(event)=>stageOperation("set_font_family",{font_family:event.target.value})}/></label></>}
					</> : <span className="muted">点击画布元素后显示安全属性</span>}
					<div className="annotation-editor"><label>让 AI 调整<textarea disabled={!editable || !editCapabilities.annotation} value={annotationText} onChange={(event)=>setAnnotationText(event.target.value)} placeholder={editCapabilities.annotation ? "仅调整现有元素、排版或层级" : "当前灰度未启用 AI 批注"}/></label><button className="secondary-button" disabled={!editable || !editCapabilities.annotation || !annotationText.trim()} onClick={addAnnotation}>添加批注</button></div>
					{draft.annotations.map((annotation) => <div className="annotation-chip" key={annotation.annotation_id}><span>{annotation.page_id} · {annotation.scope}</span><small>{annotation.instruction}</small><button disabled={!editable} onClick={()=>{setDraft((current)=>({...current,annotations:current.annotations.filter((item)=>item.annotation_id!==annotation.annotation_id)}));setDirty(true);}}>移除</button></div>)}
					{visibleState.activeSession && <div className="session-progress"><strong>{editSessionStatusText(visibleState.activeSession.status)}</strong><span>revision {visibleState.activeSession.revision}</span><span>操作 {draft.pages.reduce((sum,page)=>sum+page.operations.length,0)} · 批注 {draft.annotations.length}</span>{runs.slice(-3).map((run)=><small key={run.id}>{run.phase} · {run.status}</small>)}</div>}
					{visibleState.activeSession?.status === "draft" && <div className="inspector-actions"><button className="secondary-button" disabled={!dirty || !!busy || conflict} onClick={()=>void saveDraft()}>{busy==="save"?<Loader2 className="spin" size={15}/>:null}保存草稿</button><button className="primary-button" disabled={!!busy || conflict} onClick={()=>void applyDraft()}>应用并导出</button><button className="text-button" disabled={!!busy} onClick={()=>void api.discardEditSession(taskId,visibleState.activeSession!.id).then(()=>setActiveSession(null))}>放弃</button></div>}
					{visibleState.activeSession?.status === "failed" && <div className="failure-actions"><span className="bad">{visibleState.activeSession.failure_phase}: {visibleState.activeSession.error_message}</span><button className="secondary-button" onClick={()=>void api.retryEditSession(taskId,visibleState.activeSession!.id,visibleState.activeSession!.failure_phase).then(setActiveSession)}>技术重试</button><button className="secondary-button" onClick={()=>void api.cloneEditSession(taskId,visibleState.activeSession!.id).then(setActiveSession)}>复制草稿修正</button></div>}
				</aside>
			</div>
		</section>
	);
}

function ConfirmationField({
  confirmation,
  value,
  locked = false,
  onChange,
}: {
  confirmation: Confirmation;
  value: unknown;
  locked?: boolean;
  onChange: (value: unknown) => void;
}) {
  const options = parseJSON<string[]>(confirmation.options_json, []);
  const stringValue = String(value ?? confirmation.recommendation ?? "");

  return (
    <div className="confirm-field">
      <div className="confirm-heading">
        <span>{confirmation.label}</span>
        {locked ? <small>已锁定</small> : confirmation.required && <small>必填</small>}
      </div>
      {locked ? (
        <input value={stringValue} readOnly aria-readonly="true" />
      ) : options.length > 0 ? (
        <div className="segmented">
          {options.map((option) => (
            <button
              key={option}
              className={stringValue === option ? "segment active" : "segment"}
              onClick={() => onChange(option)}
              type="button"
            >
              {option}
            </button>
          ))}
        </div>
      ) : (
        <input value={stringValue} onChange={(event) => onChange(event.target.value)} />
      )}
      {confirmation.recommendation && <p>{confirmation.recommendation}</p>}
    </div>
  );
}

function defaultConfirmationValues(confirmations: Confirmation[]) {
  const values: Record<string, unknown> = {};
  for (const confirmation of confirmations) {
    const stored = parseJSON<unknown>(confirmation.value_json, undefined);
    if (stored !== undefined && stored !== null) {
      values[confirmation.key] = stored;
      continue;
    }
    const options = parseJSON<string[]>(confirmation.options_json, []);
    values[confirmation.key] = confirmation.recommendation || options[0] || "";
  }
  return values;
}

function confirmationSubmissionValues(task: Task | null, values: Record<string, unknown>) {
  const payload = { ...values };
  if (task?.route === "beautify" && task.status === "awaiting_realization_confirm") {
    delete payload.page_count;
    delete payload.slide_count;
  }
  return payload;
}

function specSummaryRows(summary: Record<string, unknown>) {
  const keys: Array<[string, string]> = [
    ["page_count", "页数"],
    ["canvas", "画布"],
    ["visual_style", "风格"],
    ["selected_template_id", "模板"],
    ["color", "色彩"],
    ["typography", "字体"],
    ["icons", "图标"],
    ["image_usage", "图片"],
  ];
  return keys
    .map(([key, label]) => ({ label, value: summaryValue(summary[key]) }))
    .filter((row) => row.value !== "-");
}

function summaryValue(value: unknown) {
  if (Array.isArray(value)) {
    return value.length ? value.join(", ") : "-";
  }
  if (value === undefined || value === null || value === "") {
    return "-";
  }
  if (typeof value === "object") {
    return JSON.stringify(value);
  }
  return String(value);
}

function PageHeader({
  title,
  subtitle,
  actions,
}: {
  title: string;
  subtitle?: string;
  actions?: React.ReactNode;
}) {
  return (
    <header className="page-header">
      <div>
        <h1>{title}</h1>
        {subtitle && <p>{subtitle}</p>}
      </div>
      {actions && <div className="header-actions">{actions}</div>}
    </header>
  );
}

function Metric({ label, value }: { label: string; value: number }) {
  return (
    <div className="metric">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

function StatusPill({ status, large = false }: { status: TaskStatus; large?: boolean }) {
  return <span className={`status-pill ${statusTone[status]} ${large ? "large" : ""}`}>{statusLabel[status]}</span>;
}

function IconButton({ label, children, onClick }: { label: string; children: React.ReactNode; onClick: () => void }) {
  return (
    <button className="icon-button" aria-label={label} title={label} onClick={onClick}>
      {children}
    </button>
  );
}

function InlineState({ icon, text, bad = false }: { icon: React.ReactNode; text: string; bad?: boolean }) {
  return (
    <div className={bad ? "inline-state bad" : "inline-state"} role={bad ? "alert" : "status"}>
      {icon}
      <span>{text}</span>
    </div>
  );
}
