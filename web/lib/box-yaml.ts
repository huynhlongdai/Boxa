import type { Box } from "./types";

/** Dựng box.yaml (chỉ đọc) từ object Box — contract v1 chưa có endpoint trả YAML gốc. */
export function boxToYaml(b: Box): string {
  const lines = [
    `slug: ${b.slug}`,
    `template: ${b.template}`,
    `lifecycle: ${b.lifecycle}`,
    `spec:`,
    `  cpu: ${b.spec.cpu}`,
    `  ram_mb: ${b.spec.ram_mb}`,
    `  disk_gb: ${b.spec.disk_gb}`,
  ];
  if (b.ttl_seconds) lines.push(`ttl_seconds: ${b.ttl_seconds}`);
  if (b.idle_minutes) lines.push(`idle_minutes: ${b.idle_minutes}`);
  if (b.budget_usd) lines.push(`budget_usd: ${b.budget_usd}`);
  lines.push(`ports: [${b.ports.join(", ")}]`);
  lines.push(`secrets: [${b.secrets.join(", ")}]`);
  return lines.join("\n") + "\n";
}

/** YAML để lưu thành template: bỏ slug, thêm name. */
export function boxToTemplateYaml(b: Box, name: string): string {
  return boxToYaml(b).replace(/^slug: .*\n/, `name: ${name}\n`);
}
