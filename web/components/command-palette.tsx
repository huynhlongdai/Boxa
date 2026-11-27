"use client";
// Command palette ⌘K: điều hướng, `wake <slug>`, `sleep <slug>`, `kill all`, `cost today`.
// Động từ tiếng Anh luôn dùng được; thêm bí danh theo ngôn ngữ đang chọn (vd "đánh thức", "唤醒", "起動").
import * as React from "react";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { Command } from "cmdk";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { Moon, OctagonX, Rocket, Sun, Wallet, Search } from "lucide-react";
import { toast } from "sonner";
import { useSWRConfig } from "swr";
import { api, paths } from "@/lib/api";
import { useApi } from "@/lib/hooks";
import { useFmt } from "@/lib/format";
import { strip } from "@/lib/text";
import type { Box, List, Overview } from "@/lib/types";
import { NAV, visibleNav } from "./nav";
import { useSession } from "@/lib/session";
import { PERMS } from "@/lib/types";
import { useEmergency } from "./boxmes/emergency";
import { BoxStatusChip } from "./boxmes/status";
import { toastError } from "@/lib/error-toast";

const Ctx = React.createContext<{ open: () => void }>({ open: () => {} });
export const usePalette = () => React.useContext(Ctx);


function aliasList(raw: string, canonical: string[]) {
  const all = [...canonical, ...raw.split("|")].map(strip).filter(Boolean);
  // dài trước để "cho ngu" khớp trước "ngu"
  return Array.from(new Set(all)).sort((a, b) => b.length - a.length);
}

function escapeRe(s: string) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

export function CommandPaletteProvider({ children }: { children: React.ReactNode }) {
  const [open, setOpen] = React.useState(false);
  const [q, setQ] = React.useState("");
  const router = useRouter();
  const emergency = useEmergency();
  const t = useTranslations("palette");
  const tn = useTranslations("nav");
  const tb = useTranslations("boxActions");
  const { usd } = useFmt();
  const { mutate } = useSWRConfig();
  const { can, platformAdmin } = useSession();
  const { data: boxes } = useApi<List<Box>>(open ? paths.boxes() : null);
  const { data: ov } = useApi<Overview>(open ? paths.overview : null);

  React.useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setOpen((o) => !o);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const close = () => {
    setOpen(false);
    setQ("");
  };

  const wakeAliases = React.useMemo(() => aliasList(t("aliases.wake"), ["wake"]), [t]);
  const sleepAliases = React.useMemo(() => aliasList(t("aliases.sleep"), ["sleep"]), [t]);
  const killAliases = t("aliases.kill");
  const costAliases = t("aliases.cost");

  const sq = strip(q);
  let verb: "wake" | "sleep" | null = null;
  let term = "";
  for (const [v, list] of [["wake", wakeAliases], ["sleep", sleepAliases]] as const) {
    for (const a of list) {
      const sep = /^[\x00-\x7f]+$/.test(a) ? "\\s+" : "\\s*";
      const m = sq.match(new RegExp(`^${escapeRe(a)}(?:${sep}(.*)|$)`));
      if (m) {
        verb = v;
        term = (m[1] ?? "").trim();
        break;
      }
    }
    if (verb) break;
  }
  const candidates = verb
    ? (boxes?.items ?? []).filter((b) => (verb === "wake" ? ["sleeping", "failed"] : ["running"]).includes(b.status) && b.slug.includes(term))
    : [];

  const act = async (b: Box, v: "wake" | "sleep") => {
    close();
    try {
      await (v === "wake" ? api.wakeBox(b.id) : api.sleepBox(b.id));
      toast.success(v === "wake" ? tb("waking", { slug: b.slug }) : tb("slept", { slug: b.slug }));
      mutate((k) => typeof k === "string" && k.startsWith("/boxes"));
      mutate("/overview");
    } catch (e) {
      toastError(e);
    }
  };

  const item = "flex cursor-pointer items-center gap-2.5 rounded-md px-2 py-2 text-sm aria-disabled:opacity-50 [&_svg]:size-4 [&_svg]:shrink-0 [&_svg]:text-muted-foreground";
  const hint = "ml-auto shrink-0 text-xs text-muted-foreground";

  return (
    <Ctx.Provider value={{ open: () => setOpen(true) }}>
      {children}
      <DialogPrimitive.Root open={open} onOpenChange={(o) => (o ? setOpen(true) : close())}>
        <DialogPrimitive.Portal>
          <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/60" />
          <DialogPrimitive.Content className="fixed left-1/2 top-[12dvh] z-50 w-[calc(100%-1.5rem)] max-w-xl -translate-x-1/2 overflow-hidden rounded-lg border border-border bg-popover shadow-2xl">
            <DialogPrimitive.Title className="sr-only">{t("title")}</DialogPrimitive.Title>
            <Command label={t("title")} shouldFilter={!verb} filter={(value, search) => (strip(value).includes(strip(search)) ? 1 : 0)}>
              <div className="flex items-center gap-2 border-b border-border px-3">
                <Search className="size-4 shrink-0 text-muted-foreground" />
                <Command.Input
                  value={q}
                  onValueChange={setQ}
                  autoFocus
                  placeholder={t("placeholder")}
                  className="h-12 w-full min-w-0 bg-transparent text-sm outline-none placeholder:text-muted-foreground"
                />
                <kbd className="hidden rounded border border-border px-1.5 font-mono text-[10px] text-muted-foreground sm:block">ESC</kbd>
              </div>
              <Command.List className="max-h-[60dvh] overflow-y-auto p-1.5">
                <Command.Empty className="px-3 py-6 text-center text-sm text-muted-foreground">
                  {verb ? t("emptyVerb", { verb, term }) : t("empty")}
                </Command.Empty>
                {verb ? (
                  <Command.Group heading={verb === "wake" ? t("groupWake") : t("groupSleep")}>
                    {candidates.map((b) => (
                      <Command.Item key={b.id} value={`${verb} ${b.slug}`} onSelect={() => act(b, verb!)} className={item}>
                        {verb === "wake" ? <Sun /> : <Moon />}
                        <span className="min-w-0 truncate font-mono">{b.slug}</span>
                        <span className="ml-auto shrink-0"><BoxStatusChip status={b.status} /></span>
                      </Command.Item>
                    ))}
                  </Command.Group>
                ) : (
                  <>
                    <Command.Group heading={t("groupQuick")}>
                      <Command.Item value={`cost ${t("costToday")} ${costAliases}`} className={item} onSelect={() => {
                        close();
                        if (ov) toast.info(t("costToast", { spent: usd(ov.spent_today), budget: usd(ov.budget_day) }));
                        router.push("/costs");
                      }}>
                        <Wallet /> <span className="font-mono">{t("costToday")}</span>
                        {ov && <span className={`${hint} font-mono`}>{usd(ov.spent_today)} / {usd(ov.budget_day)}</span>}
                      </Command.Item>
                      <Command.Item value={`wake ${wakeAliases.join(" ")}`} className={item} onSelect={() => setQ("wake ")}>
                        <Sun /> <span className="font-mono">wake &lt;slug&gt;</span>
                        <span className={hint}>{t("wakeDesc")}</span>
                      </Command.Item>
                      <Command.Item value={`sleep ${sleepAliases.join(" ")}`} className={item} onSelect={() => setQ("sleep ")}>
                        <Moon /> <span className="font-mono">sleep &lt;slug&gt;</span>
                        <span className={hint}>{t("sleepDesc")}</span>
                      </Command.Item>
                      {can(PERMS.emergency) && <Command.Item value={`kill all ${killAliases}`} className={`${item} text-st-error [&_svg]:text-st-error`} onSelect={() => { close(); emergency.open(); }}>
                        <OctagonX /> <span className="font-mono">kill all</span>
                        <span className="ml-auto shrink-0 text-xs">{t("killDesc")}</span>
                      </Command.Item>}
                    </Command.Group>
                    <Command.Group heading={t("groupGoto")}>
                      {NAV.filter((n) => !n.soon && visibleNav(n, can, platformAdmin)).map((n) => (
                        <Command.Item key={n.href} value={`${t("goto")} go ${tn(n.key)} ${n.key} ${n.href}${n.key === "developers" ? ` ${t("aliases.developers")}` : ""}`} className={item} onSelect={() => { close(); router.push(n.href); }}>
                          <n.icon /> {tn(n.key)}
                        </Command.Item>
                      ))}
                      <Command.Item value={`${t("goto")} onboarding ${tn("onboarding")}`} className={item} onSelect={() => { close(); router.push("/onboarding"); }}>
                        <Rocket /> {tn("onboarding")}
                      </Command.Item>
                    </Command.Group>
                    {(boxes?.items.length ?? 0) > 0 && (
                      <Command.Group heading={t("groupBoxes")}>
                        {boxes!.items.slice(0, 12).map((b) => (
                          <Command.Item key={b.id} value={`box ${b.slug} ${b.agent_name}`} className={item} onSelect={() => { close(); router.push(`/boxes/${b.id}`); }}>
                            <span className="min-w-0 truncate font-mono">{b.slug}</span>
                            <span className="hidden truncate text-xs text-muted-foreground sm:inline">{b.agent_name}</span>
                            <span className="ml-auto shrink-0"><BoxStatusChip status={b.status} /></span>
                          </Command.Item>
                        ))}
                      </Command.Group>
                    )}
                  </>
                )}
              </Command.List>
            </Command>
          </DialogPrimitive.Content>
        </DialogPrimitive.Portal>
      </DialogPrimitive.Root>
    </Ctx.Provider>
  );
}
