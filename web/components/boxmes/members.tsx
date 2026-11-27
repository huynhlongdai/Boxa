"use client";
// v2.0 Cài đặt › Thành viên & lời mời (spec §14): danh sách thành viên, đổi vai trò, xoá, rời workspace;
// mời (email + vai trò), lời mời đang chờ / đã nhận / hết hạn, gửi lại, thu hồi. Quy tắc "owner cuối cùng" hiện rõ.
// Quyền do Core quyết (members:write; chỉ owner đụng tới vai trò owner); UI ẩn/khóa theo cùng quy tắc.
import * as React from "react";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { Crown, LogOut, MailPlus, RotateCw, ShieldCheck, Trash2, UserX } from "lucide-react";
import { toast } from "sonner";
import { api, paths } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";
import { useSession, useSwitchWorkspace } from "@/lib/session";
import { useFmt } from "@/lib/format";
import { PERMS, WORKSPACE_ROLES, type List, type Member, type WorkspaceInvite, type WorkspaceInviteCreated, type WorkspaceRole } from "@/lib/types";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { ConfirmButton } from "./confirm";
import { CopyButton } from "./code-block";
import { Empty, ErrorBox, LoadingRows } from "./page";
import { useRoleLabel } from "./workspace";

function RoleSelect({ value, onChange, disabled, allowOwner, label }: { value: string; onChange: (r: WorkspaceRole) => void; disabled?: boolean; allowOwner: boolean; label: string }) {
  const roleLabel = useRoleLabel();
  return (
    <Select value={value} disabled={disabled} onChange={(e) => onChange(e.target.value as WorkspaceRole)} aria-label={label} className="h-8 w-36 text-[13px]">
      {WORKSPACE_ROLES.filter((r) => allowOwner || r !== "owner" || value === "owner").map((r) => (
        <option key={r} value={r} disabled={r === "owner" && !allowOwner}>{roleLabel(r)}</option>
      ))}
    </Select>
  );
}

function MembersCard() {
  const t = useTranslations("members");
  const tc = useTranslations("common");
  const router = useRouter();
  const roleLabel = useRoleLabel();
  const { relTime } = useFmt();
  const { me, can, platformAdmin, refresh } = useSession();
  const switchTo = useSwitchWorkspace();
  const { run, pending } = useAction();
  const { data, error, isLoading, mutate } = useApi<List<Member>>(paths.members);
  const manage = can(PERMS.members);
  const iAmOwner = platformAdmin || me?.role === "owner";
  const owners = data?.items.filter((m) => m.role === "owner").length ?? 0;
  const myId = me?.user?.id;
  const wsName = me?.workspace?.name ?? "";

  const leave = async () => {
    if (!myId) return;
    const r = await run("leave", () => api.removeMember(myId), { success: t("left", { name: wsName }) });
    if (!r) return;
    const other = me?.workspaces.find((w) => w.id !== me.workspace?.id);
    if (other) {
      try {
        await switchTo(other.id);
      } catch {
        /* phiên vẫn hợp lệ; banner "chưa thuộc workspace" sẽ hiện */
      }
    }
    await refresh();
    router.push("/");
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2"><ShieldCheck className="size-4" /> {t("title")}</CardTitle>
        <CardDescription>{manage ? t("descManage") : t("descView")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3 px-0 sm:px-0">
        {owners === 1 && (
          <p className="mx-4 flex items-start gap-2 rounded-md border border-st-warning/30 bg-st-warning/10 px-3 py-2 text-[12.5px] text-st-warning" data-last-owner>
            <Crown className="mt-0.5 size-4 shrink-0" /> {t("lastOwnerRule")}
          </p>
        )}
        {error ? <div className="px-4"><ErrorBox error={error} retry={() => mutate()} /></div> : isLoading || !data ? <div className="px-4"><LoadingRows rows={3} /></div> : (
          <Table>
            <THead>
              <TR className="hover:bg-transparent"><TH>{t("colMember")}</TH><TH>{t("colRole")}</TH><TH className="hidden md:table-cell">{t("colJoined")}</TH><TH className="text-right">{tc("actions")}</TH></TR>
            </THead>
            <TBody>
              {data.items.map((m) => {
                const self = m.user_id === myId;
                const isLastOwner = m.role === "owner" && owners <= 1;
                const ownerLocked = m.role === "owner" && !iAmOwner;
                return (
                  <TR key={m.user_id}>
                    <TD className="min-w-[180px]">
                      <div className="flex flex-wrap items-center gap-1.5">
                        <span className="truncate text-[13px] font-medium">{m.name || m.email}</span>
                        {self && <Badge variant="blue">{t("you")}</Badge>}
                        {!m.email_verified && <Badge variant="outline">{t("unverified")}</Badge>}
                      </div>
                      {m.name && <div className="truncate text-[12px] text-muted-foreground">{m.email}</div>}
                    </TD>
                    <TD>
                      {manage && !self ? (
                        <span title={isLastOwner ? t("lastOwnerShort") : ownerLocked ? t("ownerOnly") : undefined}>
                          <RoleSelect
                            label={t("colRole")}
                            value={m.role}
                            allowOwner={iAmOwner}
                            disabled={!!pending || isLastOwner || ownerLocked}
                            onChange={(r) => run(`role-${m.user_id}`, () => api.setMemberRole(m.user_id, r), { success: t("roleChanged", { who: m.email, role: roleLabel(r) }), revalidate: [paths.members, paths.workspaceUsage] })}
                          />
                        </span>
                      ) : (
                        <Badge variant={m.role === "owner" ? "yellow" : "outline"}>{m.role === "owner" && <Crown className="size-3" />} {roleLabel(m.role)}</Badge>
                      )}
                    </TD>
                    <TD className="hidden whitespace-nowrap text-[13px] text-muted-foreground md:table-cell">{relTime(m.joined_at)}</TD>
                    <TD>
                      <div className="flex justify-end gap-1">
                        {self ? (
                          <ConfirmButton
                            size="xs"
                            variant="outline"
                            destructive
                            disabled={isLastOwner}
                            title={isLastOwner ? t("lastOwnerShort") : t("leaveTitle", { name: wsName })}
                            desc={t("leaveDesc")}
                            confirmLabel={t("leave")}
                            onConfirm={leave}
                          >
                            <LogOut /> {t("leave")}
                          </ConfirmButton>
                        ) : manage ? (
                          <ConfirmButton
                            size="xs"
                            variant="outline"
                            destructive
                            disabled={isLastOwner || ownerLocked}
                            aria-label={t("remove")}
                            title={t("removeTitle", { who: m.email })}
                            desc={t("removeDesc")}
                            confirmLabel={t("remove")}
                            onConfirm={() => run(`rm-${m.user_id}`, () => api.removeMember(m.user_id), { success: t("removed", { who: m.email }), revalidate: [paths.members, paths.workspaceUsage] })}
                          >
                            <UserX />
                          </ConfirmButton>
                        ) : null}
                      </div>
                    </TD>
                  </TR>
                );
              })}
            </TBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

function InviteForm({ onCreated }: { onCreated: (r: WorkspaceInviteCreated) => void }) {
  const t = useTranslations("members");
  const { me, platformAdmin } = useSession();
  const { run, pending } = useAction();
  const [emails, setEmails] = React.useState("");
  const [role, setRole] = React.useState<WorkspaceRole>("developer");
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const list = emails.split(/[\s,;]+/).map((x) => x.trim()).filter(Boolean);
    for (const email of list) {
      const r = await run(`inv-${email}`, () => api.createInvite(email, role), { revalidate: [paths.invites] });
      if (r) onCreated(r);
      else return; // lỗi (vd QUOTA_EXCEEDED max_members) đã hiện toast; dừng lô
    }
    setEmails("");
  };
  return (
    <form onSubmit={submit} className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_auto_auto] sm:items-end">
      <Field label={t("inviteEmails")} hint={t("inviteEmailsHint")}>
        <Input value={emails} onChange={(e) => setEmails(e.target.value)} placeholder="lan@example.com, minh@example.com" />
      </Field>
      <Field label={t("colRole")}>
        <RoleSelect label={t("colRole")} value={role} onChange={setRole} allowOwner={platformAdmin || me?.role === "owner"} />
      </Field>
      <Button type="submit" disabled={!!pending || !emails.trim()} className="sm:mb-[1px]"><MailPlus /> {t("invite")}</Button>
    </form>
  );
}

function InviteStatusBadge({ s }: { s: WorkspaceInvite["status"] }) {
  const t = useTranslations("members");
  return <Badge variant={s === "pending" ? "blue" : s === "accepted" ? "green" : "gray"}>{t(`status.${s}` as "status.pending")}</Badge>;
}

function InvitesCard() {
  const t = useTranslations("members");
  const tc = useTranslations("common");
  const roleLabel = useRoleLabel();
  const { relTime, dateTime } = useFmt();
  const { run, pending } = useAction();
  const { data, error, isLoading, mutate } = useApi<List<WorkspaceInvite>>(paths.invites);
  const [created, setCreated] = React.useState<WorkspaceInviteCreated[]>([]);
  const onCreated = (r: WorkspaceInviteCreated) => {
    setCreated((x) => [r, ...x.filter((y) => y.email !== r.email)]);
    toast.success(r.mailed ? t("invitedMailed", { email: r.email }) : t("invitedLink", { email: r.email }));
  };
  const rows = [...(data?.items ?? [])].sort((a, b) => (a.status === b.status ? b.created_at.localeCompare(a.created_at) : a.status === "pending" ? -1 : b.status === "pending" ? 1 : 0));
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2"><MailPlus className="size-4" /> {t("invitesTitle")}</CardTitle>
        <CardDescription>{t("invitesDesc")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <InviteForm onCreated={onCreated} />
        {created.length > 0 && (
          <div className="flex flex-col gap-2 rounded-md border border-primary/30 bg-primary/5 p-3" data-invite-links>
            <p className="text-[12.5px] font-medium">{t("linksOnce")}</p>
            {created.map((c) => (
              <div key={c.id} className="flex min-w-0 items-center gap-2 text-[12.5px]">
                <span className="w-40 shrink-0 truncate">{c.email}</span>
                <code className="min-w-0 flex-1 truncate rounded bg-surface-2 px-1.5 py-0.5 font-mono text-[11.5px]">{c.invite_url}</code>
                <CopyButton text={c.invite_url} />
              </div>
            ))}
          </div>
        )}
        {error ? <ErrorBox error={error} retry={() => mutate()} /> : isLoading || !data ? <LoadingRows rows={2} /> : !rows.length ? <Empty title={t("noInvites")} /> : (
          <div className="-mx-4">
            <Table>
              <THead>
                <TR className="hover:bg-transparent"><TH>{t("colEmail")}</TH><TH>{t("colRole")}</TH><TH>{t("colStatus")}</TH><TH className="hidden md:table-cell">{t("colExpires")}</TH><TH className="text-right">{tc("actions")}</TH></TR>
              </THead>
              <TBody>
                {rows.map((i) => (
                  <TR key={i.id} data-invite-status={i.status}>
                    <TD className="max-w-[220px] truncate text-[13px]">{i.email}</TD>
                    <TD><Badge variant="outline">{roleLabel(i.role)}</Badge></TD>
                    <TD><InviteStatusBadge s={i.status} /></TD>
                    <TD className="hidden whitespace-nowrap text-[13px] text-muted-foreground md:table-cell" title={dateTime(i.expires_at)}>
                      {i.status === "accepted" ? (i.accepted_at ? relTime(i.accepted_at) : "—") : dateTime(i.expires_at)}
                    </TD>
                    <TD>
                      <div className="flex justify-end gap-1">
                        {i.status !== "accepted" && (
                          <Button size="xs" variant="outline" disabled={!!pending} title={t("resend")} aria-label={t("resend")}
                            onClick={async () => { const r = await run(`re-${i.id}`, () => api.createInvite(i.email, i.role), { revalidate: [paths.invites] }); if (r) onCreated(r); }}>
                            <RotateCw />
                          </Button>
                        )}
                        <ConfirmButton size="xs" variant="outline" destructive aria-label={t("revoke")} title={t("revokeTitle", { email: i.email })} desc={i.status === "pending" ? t("revokeDesc") : undefined} confirmLabel={t("revoke")}
                          onConfirm={() => run(`rv-${i.id}`, () => api.revokeInvite(i.id), { success: t("revoked"), revalidate: [paths.invites] })}>
                          <Trash2 />
                        </ConfirmButton>
                      </div>
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

/** Tab "Thành viên": ai cũng xem danh sách; lời mời chỉ hiện với members:write (Core: GET invites cần quyền này). */
export function MembersTab() {
  const { can, me, session } = useSession();
  const t = useTranslations("members");
  if (session?.mode === "key" && !me?.workspace) return <Empty title={t("keyMode")} />;
  return (
    <div className="flex flex-col gap-4">
      <MembersCard />
      {can(PERMS.members) && <InvitesCard />}
    </div>
  );
}
