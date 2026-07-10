import { useCallback, useEffect, useMemo, useState } from "react";
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
import { api, Artifact, Confirmation, parseJSON, RetryPhase, RuntimeRun, SpecPreview, Task, TaskEvent, TaskPhaseRun, TaskStatus, TemplateCatalogItem } from "./api";
import { formatBytes, formatTime, phaseLabel, routeLabel, statusLabel, statusTone } from "./format";
import { go, parseRoute, Route } from "./router";

const activeStatuses: TaskStatus[] = [
  "runtime_preparing",
  "source_converting",
  "realization_deriving",
  "spec_generating",
  "image_acquiring",
  "svg_generating",
  "quality_checking",
  "exporting",
  "publishing",
];

const confirmationStatuses: TaskStatus[] = ["awaiting_confirm", "awaiting_anchor_confirm", "awaiting_realization_confirm"];

function isConfirmationStatus(status?: TaskStatus) {
  return !!status && confirmationStatuses.includes(status);
}

function isWaitingStatus(status?: TaskStatus) {
  return isConfirmationStatus(status) || status === "awaiting_spec_confirm";
}

const splitRetryOptions: Array<{ phase: RetryPhase; label: string }> = [
  { phase: "spec_generate", label: "重试规格" },
  { phase: "svg_execute", label: "重试 SVG" },
  { phase: "quality_check", label: "重跑质检" },
  { phase: "finalize_export", label: "重新导出" },
  { phase: "publish", label: "重新发布" },
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

function retryOptionsForFailure(failurePhase: string): Array<{ phase: RetryPhase; label: string }> {
  const value = failurePhase.toLowerCase();
  if (value.startsWith("prepare") || value.startsWith("source") || value.startsWith("route_select")) {
    return [{ phase: "prepare", label: "重新准备" }];
  }
  return splitRetryOptions;
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
        {route.name === "task" && <TaskDetailPage taskId={route.id} />}
        {route.name === "confirm" && <ConfirmPage taskId={route.id} />}
        {route.name === "spec" && <SpecPreviewPage taskId={route.id} />}
        {route.name === "preview" && <PreviewPage taskId={route.id} />}
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
  const [task, setTask] = useState<Task | null>(null);
  const [events, setEvents] = useState<TaskEvent[]>([]);
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [runtimeRuns, setRuntimeRuns] = useState<RuntimeRun[]>([]);
  const [phaseRuns, setPhaseRuns] = useState<TaskPhaseRun[]>([]);
  const [retrying, setRetrying] = useState<RetryPhase | "">("");
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      const [nextTask, nextEvents, nextArtifacts, nextRuntimeRuns, nextPhaseRuns] = await Promise.all([
        api.getTask(taskId),
        api.listEvents(taskId),
        api.listArtifacts(taskId),
        api.listRuntimeRuns(taskId),
        api.listPhaseRuns(taskId),
      ]);
      setTask(nextTask);
      setEvents(nextEvents);
      setArtifacts(nextArtifacts);
      setRuntimeRuns(nextRuntimeRuns);
      setPhaseRuns(nextPhaseRuns);
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

  const pptx = artifacts.find((artifact) => artifact.kind === "pptx");
  const svgFinalCount = artifacts.filter((artifact) => artifact.kind === "svg_final").length;
  const latestRun = runtimeRuns[0];
  const failureMetadata = task ? parseJSON<Record<string, unknown>>(task.failure_metadata || "{}", {}) : {};
  const routeSelection = task ? parseJSON<Record<string, unknown>>(task.route_selection_json || "{}", {}) : {};
  const taskRoute = task?.route || "main";
  const routeConfidence = typeof routeSelection.confidence === "number" ? Math.round(routeSelection.confidence * 100) : null;
  const retryOptions = task?.status === "failed" ? retryOptionsForFailure(task.failure_phase || "") : [];

  async function retry(phase: RetryPhase) {
    if (!task || retrying) {
      return;
    }
    setRetrying(phase);
    setError("");
    try {
      const nextTask = await api.retryTask(task.id, phase);
      setTask(nextTask);
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setRetrying("");
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
            {task?.status === "completed" && (
              <button className="primary-button" onClick={() => go({ name: "preview", id: task.id })}>
                <Eye size={17} />
                <span>预览</span>
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
                {task.status === "failed" && (
                  <>
                    <span>失败阶段</span>
                    <strong className="mono">{task.failure_phase || "-"}</strong>
                  </>
                )}
              </div>
            </div>

            <div className="status-panel">
              <div className="section-title">
                <Presentation size={17} />
                <span>产物</span>
              </div>
              <div className="artifact-list">
                {artifacts.slice(0, 8).map((artifact) => (
                  <span className="artifact-chip" key={artifact.id}>
                    {artifact.kind}
                    <small>{artifact.name}</small>
                  </span>
                ))}
                {artifacts.length === 0 && <span className="muted">-</span>}
              </div>
              <div className="button-row">
                <button className="secondary-button" disabled={svgFinalCount === 0} onClick={() => go({ name: "preview", id: task.id })}>
                  <Eye size={16} />
                  <span>SVG</span>
                </button>
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
      const nextTask = await api.submitConfirmations(taskId, values);
      setTask(nextTask);
      if (nextTask.status === "awaiting_realization_confirm") {
        const nextConfirmations = await api.listConfirmations(taskId);
        setConfirmations(nextConfirmations);
        setValues(defaultConfirmationValues(nextConfirmations));
      } else if (nextTask.status === "awaiting_spec_confirm") {
        go({ name: "spec", id: taskId });
      } else if (nextTask.status === "completed") {
        go({ name: "preview", id: taskId });
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
      ? "根据你已确认的目标锚点，确认页数、色彩、字体、图片和生成模式。"
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

function PreviewPage({ taskId }: { taskId: string }) {
  const [task, setTask] = useState<Task | null>(null);
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [selectedId, setSelectedId] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    async function load() {
      try {
        const [nextTask, nextArtifacts] = await Promise.all([api.getTask(taskId), api.listArtifacts(taskId)]);
        const svg = nextArtifacts.filter((artifact) => artifact.kind === "svg_final");
        setTask(nextTask);
        setArtifacts(nextArtifacts);
        setSelectedId((current) => current || svg[0]?.id || "");
        setError("");
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      }
    }
    void load();
  }, [taskId]);

  const svgArtifacts = artifacts.filter((artifact) => artifact.kind === "svg_final");
  const selected = svgArtifacts.find((artifact) => artifact.id === selectedId) || svgArtifacts[0];
  const pptx = artifacts.find((artifact) => artifact.kind === "pptx");

  return (
    <section className="page preview-page">
      <PageHeader
        title="预览与下载"
        subtitle={task?.title || taskId}
        actions={
          <>
            <button className="secondary-button" onClick={() => go({ name: "task", id: taskId })}>
              <ArrowLeft size={16} />
              <span>返回</span>
            </button>
            <a className={pptx ? "primary-button" : "primary-button disabled"} href={pptx ? api.pptxDownloadUrl(taskId) : undefined}>
              <Download size={17} />
              <span>下载 PPTX</span>
            </a>
          </>
        }
      />

      {error && <InlineState icon={<XCircle size={18} />} text={error} bad />}
      <div className="preview-layout">
        <div className="slide-rail">
          {svgArtifacts.map((artifact, index) => (
            <button
              className={artifact.id === selected?.id ? "slide-thumb active" : "slide-thumb"}
              key={artifact.id}
              onClick={() => setSelectedId(artifact.id)}
            >
              <span>{String(index + 1).padStart(2, "0")}</span>
              <small>{artifact.name}</small>
            </button>
          ))}
          {svgArtifacts.length === 0 && <InlineState icon={<Clock3 size={18} />} text="暂无 SVG" />}
        </div>
        <div className="svg-stage">
          {selected ? (
            <img alt={selected.name} src={api.artifactContentUrl(taskId, selected.id)} />
          ) : (
            <InlineState icon={<Clock3 size={18} />} text="-" />
          )}
        </div>
      </div>
    </section>
  );
}

function ConfirmationField({
  confirmation,
  value,
  onChange,
}: {
  confirmation: Confirmation;
  value: unknown;
  onChange: (value: unknown) => void;
}) {
  const options = parseJSON<string[]>(confirmation.options_json, []);
  const stringValue = String(value ?? confirmation.recommendation ?? "");

  return (
    <div className="confirm-field">
      <div className="confirm-heading">
        <span>{confirmation.label}</span>
        {confirmation.required && <small>必填</small>}
      </div>
      {options.length > 0 ? (
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
    <div className={bad ? "inline-state bad" : "inline-state"}>
      {icon}
      <span>{text}</span>
    </div>
  );
}
