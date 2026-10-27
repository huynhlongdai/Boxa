// Mẫu adapter khai báo (YAML) cho màn hình "Adapter YAML tuỳ chỉnh" — cùng cú pháp với core/internal/sandbox/builtin/*.yaml.
export const SAMPLE_ADAPTER = `# Declarative sandbox adapter (Boxaz v1.4) — HTTP templates + JSON paths.
id: acme-sandbox
name: Acme Sandbox
tier: T2
verification: docs-verified
docs: https://docs.acme.dev/sandbox
initials: AC
color: "#7c3aed"
base_url: https://api.acme.dev/v1
auth: {Authorization: "Bearer {{cred.token}}"}
credentials: [{name: token, label: API key, secret: true, hint: "acme_…"}]
env_vars: [ACME_API_KEY]
capabilities: {background: true, pause: true, fork: false, ports: true, files: true, boot_ms: 800, max_lifetime_s: 86400, isolation: microvm}
pricing: {cpu_second: 0.000014, gb_second: 0.0000045, source: "https://acme.dev/pricing", confidence: docs}
sizes:
  - {sku: small, cpu: 1, ram_mb: 2048, disk_gb: 10}
  - {sku: medium, cpu: 2, ram_mb: 4096, disk_gb: 20}
regions: [{id: global, name: Acme}]
validate: {method: GET, path: /sandboxes}
create:
  method: POST
  path: /sandboxes
  body: {cpu: "{{int:spec.cpu}}", memory_mb: "{{int:spec.ram_mb}}"}
  capture: {id: $.id, state: $.status}
get: {method: GET, path: "/sandboxes/{{p:id}}", capture: {state: $.status}}
delete: {method: DELETE, path: "/sandboxes/{{p:id}}"}
exec:
  - method: POST
    path: "/sandboxes/{{p:id}}/exec"
    body: {command: "{{cmd}}", timeout: "{{int:timeout_s}}"}
    capture: {stdout: $.stdout, stderr: $.stderr, exit_code: $.exit_code}
`;
