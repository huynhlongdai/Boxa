"use client";
// v1.10 tab "Truy cập" của box: lệnh copy được (box forward / box ssh / ssh-config) từ GET /boxes/{id}/access,
// quản lý danh sách port forward (POST forward-ports, PATCH forward_ports), ticket đang có + thu hồi tất cả.
// Thẻ SSH keys (Settings): GET/POST/DELETE /ssh/keys + host key của gateway (GET /ssh).
import * as React from "react";
import { useTranslations } from "next-intl";
import { KeyRound, Link2, Network, Plus, ShieldOff, TerminalSquare, Ticket, Trash2, X } from "lucide-react";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import { useFmt } from "@/lib/format";
import type { AccessInfo, AccessTicket, Box, List, SshInfo, SshKey } from "@/lib/types";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input, Textarea } from "@/components/ui/input";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { CodeBlock, CopyButton } from "@/components/boxaz/code-block";
import { ConfirmButton } from "@/components/boxaz/confirm";
import { Empty, ErrorBox, LoadingRows } from "@/components/boxaz/page";
import { Mono } from "@/components/boxaz/status";

const SSH_INTERNAL_PORT = 2222;
const LINK_KEYS: Record<string, "boxd" | "agent" | "agentOnDemand" | "none"> = { boxd: "boxd", agent: "agent", "agent-on-demand": "agentOnDemand", none: "none" };

function Cmd({ label, cmd, testid }: { label: string; cmd: string; testid?: string }) {
  return (
    <div className="flex flex-col gap-1" data-testid={testid}>
      <span className="text-xs text-muted-foreground">{label}</span>
      <div className="flex items-center gap-2 rounded-md border border-border bg-background px-2.5 py-1.5">
        <code className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap font-mono text-[12.5px]">{cmd}</code>
        <CopyButton text={cmd} />
      </div>
    </div>
  );
}

type TicketState = "live" | "pending" | "redeemed" | "expired" | "revoked";
function ticketState(t: AccessTicket): TicketState {
  if (t.revoked_at) return "revoked";
  if (t.live) return "live";
  if (t.redeemed_at) return "redeemed";
  return Date.parse(t.expires_at) > Date.now() ? "pending" : "expired";
}
const STATE_VARIANT: Record<TicketState, "green" | "blue" | "gray" | "outline" | "red"> = { live: "green", pending: "blue", redeemed: "gray", expired: "outline", revoked: "red" };

// ---------------- Lệnh nhanh + ssh-config ----------------
function CommandsCard({ box, info }: { box: Box; info: AccessInfo }) {
  const t = useTranslations("access");
  const name = info.name || box.slug;
  const firstPort = info.tcp.allowed_ports.find((p) => p !== SSH_INTERNAL_PORT) ?? 3000;
  const [port, setPort] = React.useState(String(firstPort));
  const [local, setLocal] = React.useState("");
  const p = /^\d{1,5}$/.test(port) ? port : "<port>";
  const fwd = `box forward ${name} ${p}${/^\d{1,5}$/.test(local) ? ` ${local}` : ""}`;
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2"><TerminalSquare className="size-4 text-primary" /> {t("commands")}</CardTitle>
        <CardDescription>{t("commandsDesc")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="grid grid-cols-2 gap-2 sm:max-w-sm">
          <Field label={t("remotePort")}><Input value={port} onChange={(e) => setPort(e.target.value.trim())} inputMode="numeric" className="font-mono" data-testid="access-forward-port" /></Field>
          <Field label={t("localPort")}><Input value={local} onChange={(e) => setLocal(e.target.value.trim())} inputMode="numeric" placeholder={p} className="font-mono" /></Field>
        </div>
        <Cmd label={t("cmdForward")} cmd={fwd} testid="access-cmd-forward" />
        <Cmd label={t("cmdSsh")} cmd={info.commands.ssh || `box ssh ${name}`} testid="access-cmd-ssh" />
        <Cmd label={t("cmdSshExec")} cmd={info.commands.ssh_exec || `box ssh ${name} -- uname -a`} />
        <Cmd label={t("cmdSshL")} cmd={`box ssh ${name} -L ${/^\d{1,5}$/.test(local) ? local : p}:localhost:${p} -N`} />
        {info.commands.ssh_proxy && <Cmd label={t("cmdOpenSsh")} cmd={info.commands.ssh_proxy} />}
        {info.commands.ssh_direct && <Cmd label={t("cmdDirect")} cmd={info.commands.ssh_direct} />}
        <p className="text-xs text-muted-foreground">{t("cliHint")}</p>
      </CardContent>
    </Card>
  );
}

function SshConfigCard({ info }: { info: AccessInfo }) {
  const t = useTranslations("access");
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2"><KeyRound className="size-4 text-primary" /> {t("sshConfig")}</CardTitle>
        <CardDescription>{t("sshConfigDesc")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3" data-testid="access-ssh-config">
        <CodeBlock code={info.ssh.config.trimEnd()} language="~/.ssh/config" maxHeight={220} />
        <Cmd label={t("writeConfig")} cmd={info.commands.ssh_config || "box ssh-config --write"} />
        <div className="text-[13px]">
          <p className="text-xs text-muted-foreground">{t("hostKey")}</p>
          <Mono className="break-all">{info.ssh.host_key_fingerprint || "—"}</Mono>
        </div>
        {info.ssh.known_hosts && <CodeBlock code={info.ssh.known_hosts.trim()} language="~/.boxaz/known_hosts" maxHeight={120} />}
        <p className="text-xs text-muted-foreground">{t("features", { features: info.ssh.features })}</p>
      </CardContent>
    </Card>
  );
}

// ---------------- Port forward ----------------
function ForwardPortsCard({ box, info }: { box: Box; info: AccessInfo }) {
  const t = useTranslations("access");
  const { run, pending } = useAction();
  const [port, setPort] = React.useState("");
  const extra = new Set(box.forward_ports ?? []);
  const exposed = new Set(box.ports ?? []);
  const revalidate = [paths.boxAccess(box.id), paths.box(box.id)];
  const n = Number(port);
  const valid = Number.isInteger(n) && n >= 1 && n <= 65535;
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2"><Network className="size-4 text-primary" /> {t("forwardPorts")}</CardTitle>
        <CardDescription>{t("forwardPortsDesc")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-wrap gap-1.5" data-testid="access-forward-ports">
          {info.tcp.allowed_ports.map((p) => {
            const removable = extra.has(p) && !exposed.has(p) && p !== SSH_INTERNAL_PORT;
            return (
              <Badge key={p} variant={removable ? "blue" : "outline"} className="gap-1 font-mono">
                {p}
                <span className="font-sans text-[10.5px] text-muted-foreground">{p === SSH_INTERNAL_PORT ? t("portSsh") : exposed.has(p) ? t("portExposed") : t("portForward")}</span>
                {removable && (
                  <button
                    type="button" aria-label={t("removePort", { port: p })} className="ml-0.5 rounded hover:text-st-error" disabled={!!pending}
                    onClick={() => run(`rm-${p}`, () => api.setForwardPorts(box.id, [...extra].filter((x) => x !== p)), { success: t("removedPort", { port: p }), revalidate })}
                  >
                    <X className="size-3" />
                  </button>
                )}
              </Badge>
            );
          })}
        </div>
        <form
          className="flex items-end gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (!valid) return;
            run("add-port", () => api.allowForwardPort(box.id, n), { success: t("addedPort", { port: n }), revalidate }).then((r) => r && setPort(""));
          }}
        >
          <Field label={t("addPort")} className="w-36"><Input value={port} onChange={(e) => setPort(e.target.value.trim())} inputMode="numeric" placeholder="5432" className="font-mono" data-testid="access-add-port" /></Field>
          <Button type="submit" size="sm" disabled={!valid || !!pending}><Plus /> {t("allow")}</Button>
        </form>
        <p className="text-xs text-muted-foreground">{t("forwardHint")}</p>
      </CardContent>
    </Card>
  );
}

// ---------------- Ticket + phiên đang mở ----------------
function TicketsCard({ box, info }: { box: Box; info: AccessInfo }) {
  const t = useTranslations("access");
  const f = useFmt();
  const { run, pending } = useAction();
  const { data, error, mutate } = useApi<List<AccessTicket>>(paths.boxTickets(box.id), { refreshInterval: 15000 });
  const revalidate = [paths.boxTickets(box.id), paths.boxAccess(box.id), paths.boxTerminals(box.id)];
  const items = [...(data?.items ?? [])].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));
  const active = items.filter((x) => !x.revoked_at && (x.live || Date.parse(x.expires_at) > Date.now())).length;
  const live = Object.entries(info.live ?? {});
  return (
    <Card>
      <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <CardTitle className="flex items-center gap-2"><Ticket className="size-4 text-primary" /> {t("tickets")}</CardTitle>
          <CardDescription>{t("ticketsDesc")}</CardDescription>
        </div>
        <ConfirmButton
          size="sm" variant="outline" destructive disabled={!items.some((x) => !x.revoked_at)} data-testid="access-revoke-all"
          title={t("revokeAllTitle")} desc={t("revokeAllDesc")} confirmLabel={t("revokeAll")}
          onConfirm={() => run("revoke-all", () => api.revokeTicket(box.id, "all"), { success: (r) => t("revokedN", { n: r.revoked }), revalidate })}
        >
          <ShieldOff /> {t("revokeAll")}
        </ConfirmButton>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-wrap gap-1.5 text-[13px]" data-testid="access-live">
          <Badge variant="outline">{t("activeTickets", { n: active })}</Badge>
          <Badge variant="outline">{t("terminalSessions", { n: info.terminal.sessions.length })}</Badge>
          {live.map(([k, v]) => <Badge key={k} variant="green">{t("liveKind", { kind: k, n: v })}</Badge>)}
        </div>
        {error ? <ErrorBox error={error} retry={() => mutate()} /> : !data ? <LoadingRows rows={3} /> : !items.length ? <Empty title={t("noTickets")} /> : (
          <div className="overflow-x-auto" data-testid="access-tickets">
            <Table>
              <THead><TR><TH>{t("colTicket")}</TH><TH>{t("colPurpose")}</TH><TH>{t("colState")}</TH><TH>{t("colCreated")}</TH><TH>{t("colActor")}</TH><TH /></TR></THead>
              <TBody>
                {items.slice(0, 30).map((x) => {
                  const s = ticketState(x);
                  return (
                    <TR key={x.id}>
                      <TD><Mono className="text-[12px]">{x.prefix}…</Mono></TD>
                      <TD><Mono>{x.purpose}{x.port ? `:${x.port}` : ""}</Mono>{x.session_id && <span className="block font-mono text-[11px] text-muted-foreground">{x.session_id}</span>}</TD>
                      <TD><Badge variant={STATE_VARIANT[s]}>{t(`state.${s}`)}</Badge></TD>
                      <TD className="whitespace-nowrap text-muted-foreground">{f.relTime(x.created_at)}</TD>
                      <TD><Mono className="text-[12px] text-muted-foreground">{x.actor || "—"}</Mono></TD>
                      <TD className="text-right">
                        {!x.revoked_at && (
                          <Button size="xs" variant="ghost" disabled={!!pending} onClick={() => run(`rv-${x.id}`, () => api.revokeTicket(box.id, x.id), { success: t("revoked"), revalidate })}>
                            {t("revoke")}
                          </Button>
                        )}
                      </TD>
                    </TR>
                  );
                })}
              </TBody>
            </Table>
          </div>
        )}
        <p className="text-xs text-muted-foreground">{t("ticketsHint")}</p>
      </CardContent>
    </Card>
  );
}

export function AccessTab({ box }: { box: Box }) {
  const t = useTranslations("access");
  const { data: info, error, mutate } = useApi<AccessInfo>(paths.boxAccess(box.id), { refreshInterval: 20000 });
  if (error) return <ErrorBox error={error} retry={() => mutate()} />;
  if (!info) return <LoadingRows />;
  return (
    <div className="flex flex-col gap-4" data-testid="access-panel">
      <div className="flex flex-wrap items-center gap-2 text-[13px]">
        <Badge variant={info.link === "none" ? "red" : "green"} title={t(`linkHint.${LINK_KEYS[info.link] ?? "none"}`)}>
          <Link2 className="size-3" /> {t("link", { link: info.link })}
        </Badge>
        {info.host && <Badge variant="outline" className="font-mono">{info.host}</Badge>}
        <span className="text-muted-foreground">{t("ticketTtl")}</span>
      </div>
      {info.notes?.map((n) => (
        <p key={n} className="rounded-md border border-st-warning/30 bg-st-warning/5 p-2 text-[12.5px] text-st-warning">{n}</p>
      ))}
      <div className="grid grid-cols-1 items-start gap-4 lg:grid-cols-2 [&>*]:min-w-0">
        <div className="flex min-w-0 flex-col gap-4">
          <CommandsCard box={box} info={info} />
          <ForwardPortsCard box={box} info={info} />
        </div>
        <div className="flex min-w-0 flex-col gap-4">
          <SshConfigCard info={info} />
          <TicketsCard box={box} info={info} />
        </div>
      </div>
    </div>
  );
}

// ---------------- Settings: SSH keys ----------------
export function SshKeysCard() {
  const t = useTranslations("sshKeys");
  const tc = useTranslations("common");
  const f = useFmt();
  const { run, pending } = useAction();
  const { data, error, mutate } = useApi<List<SshKey>>(paths.sshKeys);
  const { data: ssh } = useApi<SshInfo>(paths.ssh, { revalidateOnFocus: false });
  const [open, setOpen] = React.useState(false);
  const [name, setName] = React.useState("");
  const [pub, setPub] = React.useState("");
  const looksOk = /^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp\d+|sk-[a-z0-9-]+@openssh\.com)\s+[A-Za-z0-9+/=]{20,}/.test(pub.trim());
  return (
    <Card id="ssh-keys" data-testid="ssh-keys">
      <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <CardTitle className="flex items-center gap-2"><KeyRound className="size-4" /> {t("title")}</CardTitle>
          <CardDescription>{t("desc")}</CardDescription>
        </div>
        <Dialog open={open} onOpenChange={setOpen}>
          <DialogTrigger asChild><Button size="sm" data-testid="ssh-key-add"><Plus /> {t("add")}</Button></DialogTrigger>
          <DialogContent className="max-w-lg">
            <DialogHeader>
              <DialogTitle>{t("addTitle")}</DialogTitle>
              <DialogDescription>{t("addDesc")}</DialogDescription>
            </DialogHeader>
            <div className="flex flex-col gap-3">
              <Field label={t("name")}><Input value={name} onChange={(e) => setName(e.target.value)} placeholder="anh@laptop" /></Field>
              <Field label={t("publicKey")} hint={t("publicKeyHint")}>
                <Textarea value={pub} onChange={(e) => setPub(e.target.value)} rows={4} className="font-mono text-[12px]" placeholder="ssh-ed25519 AAAAC3Nza… anh@laptop" data-testid="ssh-key-input" />
              </Field>
            </div>
            <DialogFooter>
              <Button variant="ghost" onClick={() => setOpen(false)}>{tc("cancel")}</Button>
              <Button
                disabled={!looksOk || !!pending}
                onClick={() => run("add-key", () => api.addSshKey({ name: name.trim(), public_key: pub.trim() }), { success: t("added"), revalidate: [paths.sshKeys] }).then((r) => {
                  if (r) { setOpen(false); setName(""); setPub(""); }
                })}
              >
                {t("add")}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {error ? <ErrorBox error={error} retry={() => mutate()} /> : !data ? <LoadingRows rows={2} /> : !data.items.length ? <Empty title={t("empty")} desc={t("emptyDesc")} /> : (
          <div className="flex flex-col divide-y divide-border/60 rounded-md border border-border">
            {data.items.map((k) => (
              <div key={k.id} className="flex items-center gap-3 px-3 py-2 text-[13px]">
                <div className="min-w-0 flex-1">
                  <p className="truncate font-medium">{k.name}</p>
                  <Mono className="block truncate text-[12px] text-muted-foreground">{k.fingerprint}</Mono>
                </div>
                <span className="hidden shrink-0 text-xs text-muted-foreground sm:inline">{f.relTime(k.created_at)}</span>
                <ConfirmButton
                  size="xs" variant="ghost" destructive title={t("deleteTitle", { name: k.name })} desc={t("deleteDesc")} confirmLabel={t("delete")}
                  onConfirm={() => run(`del-${k.id}`, () => api.deleteSshKey(k.id), { success: t("deleted"), revalidate: [paths.sshKeys] })}
                >
                  <Trash2 />
                </ConfirmButton>
              </div>
            ))}
          </div>
        )}
        {ssh && (
          <div className="grid gap-2 text-[13px] sm:grid-cols-2">
            <div><p className="text-xs text-muted-foreground">{t("hostKey")}</p><Mono className="break-all">{ssh.host_key_fingerprint || "—"}</Mono></div>
            <div><p className="text-xs text-muted-foreground">{t("direct")}</p><Mono className="break-all">{ssh.direct || t("directOff")}</Mono></div>
          </div>
        )}
        <p className="text-xs text-muted-foreground">{t("hint")}</p>
      </CardContent>
    </Card>
  );
}
