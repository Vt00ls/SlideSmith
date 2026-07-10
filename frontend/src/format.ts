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
  image_acquiring: "获取素材",
  svg_generating: "生成 SVG",
  quality_checking: "质量检查",
  exporting: "导出 PPTX",
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
  image_acquiring: "active",
  svg_generating: "active",
  quality_checking: "active",
  exporting: "active",
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
  image_acquire: "素材获取",
  svg_execute: "SVG 生成",
  quality_check: "质量检查",
  finalize_export: "最终导出",
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
  svg_output: "SVG 草稿",
  svg_final: "SVG 最终",
  pptx: "PPTX",
  log: "日志",
  manifest: "清单",
  other: "其他",
};

export const routeLabel: Record<string, string> = {
  main: "主生成",
  "template-fill": "模板填充",
  beautify: "PPTX 美化",
};

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
