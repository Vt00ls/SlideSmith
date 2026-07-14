import type { TaskStatus } from "./api";

export const statusLabel: Record<TaskStatus, string> = {
  created: "已创建",
  uploaded: "已上传",
  runtime_preparing: "准备运行层",
  source_converting: "资料转换",
  awaiting_confirm: "等待确认",
  awaiting_anchor_confirm: "确认目标",
  realization_deriving: "推导表现层",
  awaiting_realization_confirm: "确认表现",
  spec_generating: "生成规格",
  awaiting_spec_confirm: "审查规格",
  template_fill_planning: "生成填充计划",
  awaiting_template_fill_confirm: "审查填充计划",
  template_fill_checking: "检查填充计划",
  template_fill_applying: "填充 PPTX",
  template_fill_validating: "校验填充结果",
  image_acquiring: "正在准备图片、图标、公式与图表资源",
  svg_generating: "生成 SVG",
  quality_checking: "质量检查",
  exporting: "导出 PPTX",
  pptx_validating: "校验 PPTX",
  publishing: "发布产物",
  completed: "已完成",
  failed: "失败",
  cancelled: "已取消",
};

export const statusTone: Record<TaskStatus, "idle" | "active" | "waiting" | "done" | "bad"> = {
  created: "idle",
  uploaded: "idle",
  runtime_preparing: "active",
  source_converting: "active",
  awaiting_confirm: "waiting",
  awaiting_anchor_confirm: "waiting",
  realization_deriving: "active",
  awaiting_realization_confirm: "waiting",
  spec_generating: "active",
  awaiting_spec_confirm: "waiting",
  template_fill_planning: "active",
  awaiting_template_fill_confirm: "waiting",
  template_fill_checking: "active",
  template_fill_applying: "active",
  template_fill_validating: "active",
  image_acquiring: "active",
  svg_generating: "active",
  quality_checking: "active",
  exporting: "active",
  pptx_validating: "active",
  publishing: "active",
  completed: "done",
  failed: "bad",
  cancelled: "bad",
};

export const phaseLabel: Record<string, string> = {
  route_select: "路线选择",
  source_prepare: "资料准备",
  project_init: "项目初始化",
  template_resolve: "模板解析",
  anchor_confirm: "目标确认",
  realization_confirm: "表现确认",
  spec_generate: "规格生成",
  spec_refine: "规格调整",
  template_fill_plan: "填充计划",
  template_fill_check: "计划检查",
  template_fill_apply: "PPTX 填充",
  template_fill_validate: "结果校验",
  image_acquire: "资源准备",
  svg_execute: "SVG 生成",
  quality_check: "质量检查",
  finalize_export: "最终导出",
  pptx_validate: "PPTX 校验",
  publish: "发布产物",
};

export const artifactKindLabel: Record<string, string> = {
  source: "原始资料",
  source_markdown: "转换文本",
  source_conversion_profile: "转换记录",
  source_profile: "PPTX 资料画像",
  pptx_identity: "PPTX 视觉识别",
  pptx_slide_library: "PPTX 页面库",
  design_spec: "设计规格",
  spec_lock: "规格锁",
  template_fill_plan: "填充计划",
  template_fill_check_report: "计划检查报告",
  template_fill_validate_report: "填充校验报告",
  template_fill_readback: "填充回读文本",
  svg_output: "SVG 草稿",
  svg_final: "SVG 最终",
  pptx: "PPTX",
  log: "日志",
  manifest: "清单",
  resource_plan: "资源计划",
  resource_policy: "资源策略",
  resource_requirements: "资源需求",
  resource_manifest: "资源清单",
  resource_asset: "资源文件",
  image_analysis: "图片分析",
  image_prompt_manifest: "生图提示清单",
  image_prompt_review: "生图提示审查",
  image_query_manifest: "图片检索清单",
  image_source_manifest: "图片来源清单",
  formula_manifest: "公式清单",
  chart_data: "图表数据",
  chart_template: "图表模板",
  svg_inventory: "SVG Inventory",
  svg_resource_usage: "SVG 资源绑定",
  chart_usage: "图表绑定",
  notes_inventory: "讲稿 Inventory",
  speaker_notes: "逐页讲稿",
  svg_quality_report: "SVG 质量报告",
  chart_verify_report: "图表校验报告",
  quality_summary: "质量摘要",
  pptx_readback: "PPTX 回读",
  pptx_text_inventory: "PPTX 文本清单",
  pptx_validate_report: "PPTX 校验报告",
  rendered_pdf: "渲染 PDF",
  rendered_slide: "渲染页",
  contact_sheet: "联系表",
  visual_review_report: "视觉审查报告",
  other: "其他",
};

export const routeLabel: Record<string, string> = {
  main: "主生成",
  "template-fill": "模板填充",
  beautify: "PPTX 美化",
};

export const runnerProfileLabel: Record<string, string> = {
  "full-ppt-master": "Full PPT Master",
  "real-lite": "Real Lite（测试/降级）",
  smoke: "Smoke（测试 fixture）",
  "native-template-fill": "Native Template Fill",
};

export const runnerProfileSourceLabel: Record<string, string> = {
  deployment_default: "部署默认",
  explicit_config: "显式配置",
  legacy_manifest: "旧任务 Manifest",
  legacy_evidence: "旧任务执行证据",
};

export function taskRunnerProfileLabel(profile: string, route: string) {
  if (route === "template-fill") {
    return runnerProfileLabel["native-template-fill"];
  }
  if (!profile) {
    return "未锁定";
  }
  return runnerProfileLabel[profile] || profile;
}

export function formatTime(value?: string) {
  if (!value) {
    return "-";
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}

export function formatBytes(size: number) {
  if (size < 1024) {
    return `${size} B`;
  }
  if (size < 1024 * 1024) {
    return `${(size / 1024).toFixed(1)} KB`;
  }
  return `${(size / 1024 / 1024).toFixed(1)} MB`;
}
