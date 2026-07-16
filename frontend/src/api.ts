export type TaskStatus =
  | "created"
  | "uploaded"
  | "runtime_preparing"
  | "source_converting"
  | "awaiting_confirm"
  | "awaiting_anchor_confirm"
  | "realization_deriving"
  | "awaiting_realization_confirm"
  | "spec_generating"
  | "awaiting_spec_confirm"
  | "template_fill_planning"
  | "awaiting_template_fill_confirm"
  | "template_fill_checking"
  | "template_fill_applying"
  | "template_fill_validating"
  | "beautify_inventory_building"
  | "beautify_planning"
  | "awaiting_beautify_confirm"
  | "image_acquiring"
  | "svg_generating"
  | "quality_checking"
  | "exporting"
  | "pptx_validating"
  | "publishing"
  | "completed"
  | "failed"
  | "cancelled";

export type Task = {
  id: string;
  title: string;
  status: TaskStatus;
  runtime_project: string;
  last_runtime_run_id: string;
  last_runtime_session_id: string;
  runtime_workspace_path: string;
  selected_template_id: string;
  template_lock_json: string;
  route: "main" | "template-fill" | "beautify" | string;
  route_reason: string;
  route_standalone_workflow: string;
  route_selection_json: string;
  route_selected_at?: string;
  runner_profile: "full-ppt-master" | "real-lite" | "smoke" | "native-template-fill" | string;
  runner_profile_source: "deployment_default" | "explicit_config" | "legacy_manifest" | "legacy_evidence" | string;
  runner_profile_locked_at?: string;
  error_message: string;
  failure_phase: string;
  failure_metadata: string;
  created_at: string;
  updated_at: string;
  started_at?: string;
  completed_at?: string;
  cancelled_at?: string;
};

export type TaskEvent = {
  id: string;
  task_id: string;
  seq: number;
  type: string;
  status: string;
  message: string;
  source: string;
  payload: string;
  created_at: string;
};

export type Artifact = {
  id: string;
  task_id: string;
  kind: string;
  name: string;
  storage: string;
  object_key: string;
  mime_type: string;
  size: number;
  sha256: string;
  publish_version: string;
  metadata_json: string;
  created_at: string;
  updated_at: string;
};

export type ArtifactVersion = {
  version: string;
  status: "active" | string;
  source: "generation" | "manual_edit" | string;
  parent_version: string;
  is_latest: boolean;
  edit_session_id: string;
  edit_revision: number;
  pptx_artifact_id: string;
  artifact_manifest_sha256: string;
  activated_at?: string;
};

export type EditSessionStatus =
  | "draft" | "queued" | "materializing" | "applying_direct_edits" | "applying_annotations"
  | "svg_validating" | "quality_checking" | "exporting" | "pptx_validating" | "publishing"
  | "published" | "failed" | "stale" | "discarded";

export type EditSession = {
  id: string;
  task_id: string;
  base_publish_version: string;
  base_artifact_manifest_sha256: string;
  base_svg_inventory_sha256: string;
  status: EditSessionStatus;
  revision: number;
  draft: string;
  draft_sha256: string;
  frozen_revision: number;
  frozen_patch_sha256: string;
  result_publish_version: string;
  capability_snapshot: string;
  last_run_id: string;
  error_message: string;
  failure_phase: string;
  failure_metadata: string;
  created_at: string;
  updated_at: string;
  applied_at?: string;
  published_at?: string;
  discarded_at?: string;
};

export type EditRun = {
  id: string;
  task_id: string;
  edit_session_id: string;
  phase: string;
  attempt: number;
  runner: string;
  status: string;
  runtime_run_id: string;
  input: string;
  output: string;
  error_message: string;
  failure_metadata: string;
  started_at?: string;
  finished_at?: string;
  created_at: string;
  updated_at: string;
};

export type ManualEditTarget = {
  element_id: string;
  source_id?: string;
  tag: string;
  element_fingerprint: string;
};

export type ManualEditOperation = {
  operation_id: string;
  type: "set_text" | "translate" | "set_fill" | "set_stroke" | "set_opacity" | "set_font_size" | "set_font_family" | "set_font_weight" | "set_text_anchor";
  target: ManualEditTarget;
  value: Record<string, string | number>;
};

export type ManualEditDraft = {
  schema: "slidesmith.manual_edit_draft.v1";
  task_id?: string;
  edit_session_id?: string;
  base_publish_version?: string;
  base_artifact_manifest_sha256?: string;
  base_svg_inventory_sha256?: string;
  pages: Array<{ page_id: string; base_svg_sha256?: string; operations: ManualEditOperation[] }>;
  annotations: Array<{
    annotation_id: string;
    scope: "element" | "page";
    page_id: string;
    target?: ManualEditTarget;
    instruction: string;
    status: "pending";
  }>;
  client_updated_at?: string;
};

export type ManualEditSnapshotElement = ManualEditTarget & {
  text?: string;
  attributes: Record<string, string>;
};

export type ManualEditPageSnapshot = {
  task_id: string;
  session_id: string;
  revision: number;
  page_id: string;
  base_svg_sha256: string;
  editor_snapshot_sha256: string;
  canvas: { width: number; height: number; view_box: string };
  svg: string;
  elements: ManualEditSnapshotElement[];
  warnings: string[];
};

export type RuntimeRun = {
  id: string;
  task_id: string;
  runtime: string;
  agent: string;
  phase: string;
  command: string;
  external_run_id: string;
  external_session_id: string;
  status: string;
  exit_code?: number;
  workspace_path: string;
  raw_response: string;
  error_message: string;
  stderr_tail: string;
  failure_phase: string;
  failure_metadata: string;
  created_at: string;
  updated_at: string;
  started_at?: string;
  finished_at?: string;
};

export type PipelinePhase =
  | "route_select"
  | "source_prepare"
  | "project_init"
  | "template_resolve"
  | "anchor_confirm"
  | "realization_confirm"
  | "spec_generate"
  | "spec_refine"
  | "template_fill_plan"
  | "template_fill_check"
  | "template_fill_apply"
  | "template_fill_validate"
  | "beautify_inventory"
  | "beautify_plan"
  | "image_acquire"
  | "svg_execute"
  | "quality_check"
  | "finalize_export"
  | "pptx_validate"
  | "publish";

export type TaskPhaseRun = {
  id: string;
  task_id: string;
  phase: PipelinePhase | string;
  attempt: number;
  runner: string;
  status: string;
  started_at?: string;
  finished_at?: string;
  runtime_run_id: string;
  runtime_session_id: string;
  workspace_path: string;
  input_json: string;
  output_json: string;
  error_message: string;
  failure_metadata: string;
  created_at: string;
  updated_at: string;
};

export type RetryPhase =
  | "prepare"
  | "spec_generate"
  | "image_acquire"
  | "template_fill_plan"
  | "template_fill_check"
  | "template_fill_apply"
  | "template_fill_validate"
  | "beautify_inventory"
  | "beautify_plan"
  | "svg_execute"
  | "quality_check"
  | "finalize_export"
  | "pptx_validate"
  | "publish";

export type ContinuePhase = "spec_generate" | "svg_execute";

export type SpecPreviewFile = {
  name: string;
  path: string;
  content: string;
  size: number;
  updated_at: string;
};

export type SpecPreview = {
  task_id: string;
  project_path: string;
  design_spec: SpecPreviewFile;
  spec_lock: SpecPreviewFile;
  summary: Record<string, unknown>;
  confirmation: Record<string, unknown>;
  contract: Record<string, unknown>;
};

export type ResourceSummary = {
  total: number;
  ready: number;
  degraded: number;
  failed: number;
  pending: number;
  required_failed: number;
  bytes: number;
};

export type ResourceFallback = {
  type: string;
  reason: string;
};

export type TaskResourceItem = {
  id: string;
  page: number;
  type: string;
  purpose: string;
  required: boolean;
  acquire_via: string;
  provider: string;
  status: string;
  fallback: ResourceFallback;
  publishable: boolean;
  artifact_id?: string;
  mime_type?: string;
  size?: number;
  width?: number;
  height?: number;
  error_code?: string;
  error?: string;
};

export type TaskResources = {
  task_id: string;
  phase_status: string;
  summary: ResourceSummary;
  resources: TaskResourceItem[];
  manifest_sha256: string;
};

export type SVGCanvasSummary = {
  id: string;
  width: number;
  height: number;
};

export type SVGPageSummary = {
  page_id: string;
  page: number;
  filename: string;
  sha256: string;
  text_count: number;
  image_count: number;
  chart_count: number;
  resource_count: number;
  notes_present: boolean;
  warnings: string[];
  artifact_id?: string;
};

export type SVGBundleSummary = {
	task_id: string;
	publish_version?: string;
  phase_status: string;
  passed: boolean;
  canvas: SVGCanvasSummary;
  page_count: number;
  pages: SVGPageSummary[];
  resource_summary: Record<string, number>;
  chart_summary: Record<string, number>;
  notes: { present: boolean; page_count: number; empty_pages: number };
  errors: string[];
  warnings: string[];
  artifact_ids: Record<string, string>;
  inventory_sha256: string;
  phase_run_id: string;
};

export type QualityGateSummary = {
  blocking: number;
  error: number;
  warning: number;
  info: number;
  decision: string;
};

export type QualityFinding = {
  id: string;
  rule: string;
  severity: "blocking" | "error" | "warning" | "info" | string;
  status: string;
  stage: string;
  page_id: string;
  artifact: string;
  message: string;
  owner_phase: string;
  retry_phase: string;
};

export type TaskQuality = {
  task_id: string;
  current_gate: string;
  decision: string;
  warning_badge: number;
  svg_summary: QualityGateSummary;
  pptx_summary: QualityGateSummary;
  findings: QualityFinding[];
  chart_receipts: Array<{
    chart_id: string;
    page_id: string;
    mode: string;
    decision: string;
    checks: number;
    failures: number;
  }>;
  text_coverage: number;
  render_artifact_ids: string[];
  contact_sheet_artifact_id: string;
  readback_artifact_id: string;
  allowed_retry_phases: RetryPhase[];
  beautify_fidelity?: BeautifyFidelitySummary;
};

export type BeautifyFidelityMetric = {
  expected?: number;
  matched?: number;
  missing?: number | string[];
  changed?: number | string[];
  reordered?: number | string[];
  mismatches?: number | string[];
  required?: number;
  used?: number;
};

export type BeautifyFidelityPage = {
  source_slide: number;
  output_page: number;
  decision: string;
  text: BeautifyFidelityMetric;
  tables: BeautifyFidelityMetric;
  charts: BeautifyFidelityMetric;
  images: BeautifyFidelityMetric;
};

export type BeautifyFidelitySummary = {
  present?: boolean;
  decision: string;
  source_slide_count: number;
  output_slide_count: number;
  pages: BeautifyFidelityPage[];
  identity: {
    selected_source: string;
    overrides: string[];
    font_substitutions: string[];
  };
  ignored: string[];
  unsupported: string[];
  warning: number;
  error: number;
  blocking: number;
  report_artifact_id: string;
};

export type TemplateFillInputs = {
  project_path: string;
  source_pptx: string;
  slide_library: string;
  fill_plan: string;
  check_report: string;
  validate_report: string;
  readback: string;
  export_base: string;
  content_sources: string[];
};

export type TemplateFillPlanFile = {
  name: string;
  path: string;
  content: string;
  size: number;
  updated_at: string;
};

export type TemplateFillPlanPreview = {
  task_id: string;
  project_path: string;
  inputs: TemplateFillInputs;
  plan: Record<string, unknown>;
  check_report: Record<string, unknown>;
  summary: Record<string, unknown>;
  plan_file: TemplateFillPlanFile;
  can_edit: boolean;
  can_confirm: boolean;
};

export type BeautifySourceSummary = {
  name: string;
  sha256: string;
  slide_count: number;
  canvas: string;
};

export type BeautifyIdentitySummary = {
  selected_source: string;
  canvas: string;
  palette: string[];
  fonts: string[];
  overrides: string[];
};

export type BeautifyInventoryPageSummary = {
  source_slide: number;
  text_count: number;
  image_count: number;
  table_count: number;
  chart_count: number;
  ignored_count: number;
  unsupported_count: number;
  needs_confirmation_count: number;
};

export type BeautifyRiskSummary = {
  id: string;
  source_slide: number;
  code: string;
  severity: string;
  object_type: string;
  message: string;
  decision: string;
};

export type BeautifyPlanItemDecision = {
  id: string;
  type?: string;
  reason: string;
};

export type BeautifyPlanSlide = {
  source_slide: number;
  output_page: number;
  page_role: string;
  page_rhythm: string;
  layout_strategy: string;
  text_block_ids: string[];
  image_ids: string[];
  table_ids: string[];
  chart_ids: string[];
  ignored: BeautifyPlanItemDecision[];
  unsupported: BeautifyPlanItemDecision[];
  risks: string[];
};

export type BeautifyPlan = {
  schema: string;
  task_id: string;
  status: string;
  revision: number;
  source_pptx_sha256: string;
  inventory_sha256: string;
  confirmation_sha256: string;
  slide_count: number;
  identity: {
    source: string;
    canvas_override: boolean;
    palette_override: boolean;
    typography_override: boolean;
  };
  slides: BeautifyPlanSlide[];
  global_ignored: BeautifyPlanItemDecision[];
  accepted_risks: string[];
  created_at: string;
};

export type BeautifyPlanFinding = {
  id: string;
  severity: string;
  code: string;
  source_slide: number;
  message: string;
};

export type BeautifyPlanPreview = {
  task_id: string;
  source: BeautifySourceSummary;
  identity: BeautifyIdentitySummary;
  inventory: {
    slide_count: number;
    pages: BeautifyInventoryPageSummary[];
  };
  risks: BeautifyRiskSummary[];
  plan: BeautifyPlan;
  findings: BeautifyPlanFinding[];
  summary: Record<string, unknown>;
  plan_sha256: string;
  revision: number;
  can_edit: boolean;
  can_confirm: boolean;
};

export type Confirmation = {
  id: string;
  task_id: string;
  key: string;
  label: string;
  required: boolean;
  options_json: string;
  recommendation: string;
  value_json: string;
  status: string;
  created_at: string;
  updated_at: string;
  submitted_at?: string;
};

export type TemplatePreview = {
  name: string;
  path: string;
  url: string;
};

export type TemplateCompatibility = Record<string, string[]>;

export type TemplateCatalogItem = {
  id: string;
  kind: "layout" | "deck" | "brand" | string;
  name: string;
  display_name: string;
  version?: string;
  status?: "active" | "deprecated" | "disabled" | string;
  summary?: string;
  canvas?: string;
  default_page_count?: number;
  page_types?: string[];
  primary_color?: string;
  preview_assets?: TemplatePreview[];
  template_path?: string;
  design_spec_path?: string;
  checksum?: string;
  compatibility?: TemplateCompatibility;
};

const API_BASE = import.meta.env.VITE_API_BASE || "/api";

type Envelope<T> = {
  data: T;
  error?: string;
};

export class APIError extends Error {
  status: number;

  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${API_BASE}${path}`, {
    ...init,
    headers: {
      ...(init?.body instanceof FormData ? {} : { "Content-Type": "application/json" }),
      ...init?.headers,
    },
  });
  const text = await response.text();
  const payload = text ? (JSON.parse(text) as Envelope<T>) : undefined;
  if (!response.ok) {
    throw new APIError(payload?.error || response.statusText, response.status);
  }
  return payload?.data as T;
}

function normalizeTaskQuality(quality: TaskQuality): TaskQuality {
  const fidelity = quality.beautify_fidelity;
  return {
    ...quality,
    findings: quality.findings ?? [],
    chart_receipts: quality.chart_receipts ?? [],
    render_artifact_ids: quality.render_artifact_ids ?? [],
    allowed_retry_phases: quality.allowed_retry_phases ?? [],
    beautify_fidelity: fidelity
      ? {
          ...fidelity,
          pages: fidelity.pages ?? [],
          identity: {
            ...(fidelity.identity ?? { selected_source: "" }),
            selected_source: fidelity.identity?.selected_source ?? "",
            overrides: fidelity.identity?.overrides ?? [],
            font_substitutions: fidelity.identity?.font_substitutions ?? [],
          },
          ignored: fidelity.ignored ?? [],
          unsupported: fidelity.unsupported ?? [],
        }
      : undefined,
  };
}

export const api = {
  listTemplates: () => request<TemplateCatalogItem[]>("/templates"),
  getTemplate: (id: string) => request<TemplateCatalogItem>(`/templates/${encodeURIComponent(id)}`),
  templateAssetUrl: (url: string) => {
    if (/^https?:\/\//.test(url)) {
      return url;
    }
    if (url.startsWith("/api/")) {
      return API_BASE === "/api" ? url : `${API_BASE}${url.slice("/api".length)}`;
    }
    return url.startsWith("/") ? `${API_BASE}${url}` : `${API_BASE}/${url}`;
  },
  listTasks: () => request<Task[]>("/tasks"),
  createTask: (title: string, templateId?: string) =>
    request<Task>("/tasks", {
      method: "POST",
      body: JSON.stringify({ title, template_id: templateId || "" }),
    }),
  getTask: (id: string) => request<Task>(`/tasks/${encodeURIComponent(id)}`),
  uploadFile: (id: string, file: File) => {
    const body = new FormData();
    body.append("file", file);
    return request<Artifact>(`/tasks/${encodeURIComponent(id)}/files`, {
      method: "POST",
      body,
    });
  },
  startTask: (id: string) =>
    request<Task>(`/tasks/${encodeURIComponent(id)}/start`, {
      method: "POST",
    }),
  retryTask: (id: string, phase?: RetryPhase) =>
    request<Task>(`/tasks/${encodeURIComponent(id)}/retry`, {
      method: "POST",
      body: JSON.stringify({ phase }),
    }),
  continueTask: (id: string, phase?: ContinuePhase) =>
    request<Task>(`/tasks/${encodeURIComponent(id)}/continue`, {
      method: "POST",
      body: JSON.stringify({ phase }),
    }),
  listEvents: (id: string, afterSeq = 0) =>
    request<TaskEvent[]>(`/tasks/${encodeURIComponent(id)}/events?after_seq=${afterSeq}`),
  listRuntimeRuns: (id: string) => request<RuntimeRun[]>(`/tasks/${encodeURIComponent(id)}/runtime-runs`),
  listPhaseRuns: (id: string) => request<TaskPhaseRun[]>(`/tasks/${encodeURIComponent(id)}/phase-runs`),
  getSpecPreview: (id: string) => request<SpecPreview>(`/tasks/${encodeURIComponent(id)}/spec`),
  getTemplateFillPlan: (id: string) =>
    request<TemplateFillPlanPreview>(`/tasks/${encodeURIComponent(id)}/template-fill/plan`),
  saveTemplateFillPlan: (id: string, plan: Record<string, unknown>) =>
    request<TemplateFillPlanPreview>(`/tasks/${encodeURIComponent(id)}/template-fill/plan`, {
      method: "PUT",
      body: JSON.stringify({ plan }),
    }),
  checkTemplateFillPlan: (id: string) =>
    request<Task>(`/tasks/${encodeURIComponent(id)}/template-fill/check`, { method: "POST" }),
  confirmTemplateFillPlan: (id: string) =>
    request<Task>(`/tasks/${encodeURIComponent(id)}/template-fill/confirm`, { method: "POST" }),
  regenerateTemplateFillPlan: (id: string) =>
    request<Task>(`/tasks/${encodeURIComponent(id)}/template-fill/regenerate`, { method: "POST" }),
  getBeautifyPlan: (id: string) =>
    request<BeautifyPlanPreview>(`/tasks/${encodeURIComponent(id)}/beautify-plan`),
  saveBeautifyPlan: (id: string, plan: BeautifyPlan, expectedPlanSHA256: string) =>
    request<BeautifyPlanPreview>(`/tasks/${encodeURIComponent(id)}/beautify-plan`, {
      method: "PUT",
      body: JSON.stringify({ plan, expected_plan_sha256: expectedPlanSHA256 }),
    }),
  checkBeautifyPlan: (id: string) =>
    request<Task>(`/tasks/${encodeURIComponent(id)}/beautify-plan/check`, { method: "POST" }),
  confirmBeautifyPlan: (id: string) =>
    request<Task>(`/tasks/${encodeURIComponent(id)}/beautify-plan/confirm`, { method: "POST" }),
  regenerateBeautifyPlan: (id: string) =>
    request<Task>(`/tasks/${encodeURIComponent(id)}/beautify-plan/regenerate`, { method: "POST" }),
  listConfirmations: (id: string) =>
    request<Confirmation[]>(`/tasks/${encodeURIComponent(id)}/confirmations`),
  submitConfirmations: (id: string, values: Record<string, unknown>) =>
    request<Task>(`/tasks/${encodeURIComponent(id)}/confirmations`, {
      method: "POST",
      body: JSON.stringify({ values }),
    }),
	listArtifacts: (id: string) => request<Artifact[]>(`/tasks/${encodeURIComponent(id)}/artifacts`),
	listArtifactVersions: (id: string) => request<ArtifactVersion[]>(`/tasks/${encodeURIComponent(id)}/versions`),
	listArtifactsByVersion: (id: string, version: string) => request<Artifact[]>(`/tasks/${encodeURIComponent(id)}/versions/${encodeURIComponent(version)}/artifacts`),
	getSVGBundleByVersion: (id: string, version: string) => request<SVGBundleSummary>(`/tasks/${encodeURIComponent(id)}/versions/${encodeURIComponent(version)}/svg-bundle`),
	createEditSession: (id: string, basePublishVersion: string) => request<EditSession>(`/tasks/${encodeURIComponent(id)}/edit-sessions`, { method: "POST", body: JSON.stringify({ base_publish_version: basePublishVersion }) }),
	listEditSessions: (id: string) => request<EditSession[]>(`/tasks/${encodeURIComponent(id)}/edit-sessions`),
	getEditSession: (id: string, sessionId: string) => request<EditSession>(`/tasks/${encodeURIComponent(id)}/edit-sessions/${encodeURIComponent(sessionId)}`),
	saveEditSessionDraft: (id: string, sessionId: string, expectedRevision: number, draft: ManualEditDraft) => request<EditSession>(`/tasks/${encodeURIComponent(id)}/edit-sessions/${encodeURIComponent(sessionId)}/draft`, { method: "PUT", body: JSON.stringify({ expected_revision: expectedRevision, draft }) }),
	applyEditSession: (id: string, sessionId: string, expectedRevision: number, expectedDraftSHA256: string) => request<EditSession>(`/tasks/${encodeURIComponent(id)}/edit-sessions/${encodeURIComponent(sessionId)}/apply`, { method: "POST", body: JSON.stringify({ expected_revision: expectedRevision, expected_draft_sha256: expectedDraftSHA256 }) }),
	retryEditSession: (id: string, sessionId: string, phase = "") => request<EditSession>(`/tasks/${encodeURIComponent(id)}/edit-sessions/${encodeURIComponent(sessionId)}/retry`, { method: "POST", body: JSON.stringify({ phase }) }),
	discardEditSession: (id: string, sessionId: string) => request<EditSession>(`/tasks/${encodeURIComponent(id)}/edit-sessions/${encodeURIComponent(sessionId)}/discard`, { method: "POST", body: "{}" }),
	cloneEditSession: (id: string, sessionId: string) => request<EditSession>(`/tasks/${encodeURIComponent(id)}/edit-sessions/${encodeURIComponent(sessionId)}/clone`, { method: "POST", body: "{}" }),
	listEditRuns: (id: string, sessionId: string) => request<EditRun[]>(`/tasks/${encodeURIComponent(id)}/edit-sessions/${encodeURIComponent(sessionId)}/runs`),
	getEditSessionPage: (id: string, sessionId: string, pageId: string) => request<ManualEditPageSnapshot>(`/tasks/${encodeURIComponent(id)}/edit-sessions/${encodeURIComponent(sessionId)}/pages/${encodeURIComponent(pageId)}`),
  getResources: (id: string) => request<TaskResources>(`/tasks/${encodeURIComponent(id)}/resources`),
  getSVGBundle: (id: string) => request<SVGBundleSummary>(`/tasks/${encodeURIComponent(id)}/svg-bundle`),
  getQuality: (id: string) =>
    request<TaskQuality>(`/tasks/${encodeURIComponent(id)}/quality`).then(normalizeTaskQuality),
  artifactContentUrl: (taskId: string, artifactId: string) =>
    `${API_BASE}/tasks/${encodeURIComponent(taskId)}/artifacts/${encodeURIComponent(artifactId)}/content`,
	pptxDownloadUrl: (taskId: string) => `${API_BASE}/tasks/${encodeURIComponent(taskId)}/download/pptx`,
	versionPPTXDownloadUrl: (taskId: string, version: string) => `${API_BASE}/tasks/${encodeURIComponent(taskId)}/versions/${encodeURIComponent(version)}/download/pptx`,
};

export function parseJSON<T>(value: string, fallback: T): T {
  try {
    return JSON.parse(value) as T;
  } catch {
    return fallback;
  }
}
