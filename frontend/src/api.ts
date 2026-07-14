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
  | "image_acquiring"
  | "svg_generating"
  | "quality_checking"
  | "exporting"
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
  | "image_acquire"
  | "svg_execute"
  | "quality_check"
  | "finalize_export"
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
  | "template_fill_plan"
  | "template_fill_check"
  | "template_fill_apply"
  | "template_fill_validate"
  | "svg_execute"
  | "quality_check"
  | "finalize_export"
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
  listConfirmations: (id: string) =>
    request<Confirmation[]>(`/tasks/${encodeURIComponent(id)}/confirmations`),
  submitConfirmations: (id: string, values: Record<string, unknown>) =>
    request<Task>(`/tasks/${encodeURIComponent(id)}/confirmations`, {
      method: "POST",
      body: JSON.stringify({ values }),
    }),
  listArtifacts: (id: string) => request<Artifact[]>(`/tasks/${encodeURIComponent(id)}/artifacts`),
  artifactContentUrl: (taskId: string, artifactId: string) =>
    `${API_BASE}/tasks/${encodeURIComponent(taskId)}/artifacts/${encodeURIComponent(artifactId)}/content`,
  pptxDownloadUrl: (taskId: string) => `${API_BASE}/tasks/${encodeURIComponent(taskId)}/download/pptx`,
};

export function parseJSON<T>(value: string, fallback: T): T {
  try {
    return JSON.parse(value) as T;
  } catch {
    return fallback;
  }
}
