"use client";
// Command palette ⌘K: điều hướng, `wake <slug>`, `sleep <slug>`, `kill all`, `cost hôm nay`.
import * as React from "react";
import { useRouter } from "next/navigation";
import { Command } from "cmdk";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { Moon, OctagonX, Search, Sun, Wallet } from "lucide-react";
import { toast } from "sonner";
import { useSWRConfig } from "swr";
import { api, errMsg, paths } from "@/lib/api";
import { useApi } from "@/lib/hooks";
import type { Box, List, Overview } from "@/lib/types";
import { usd } from "@/lib/utils";
import { NAV } from "./nav";
import { useEmergency } from "./boxa/emergency";
import { BoxStatusChip } from "./boxa/status";

const Ctx = React.createContext<{ open: () => void }>({ open: () => {} });
export const usePalette = () => React.useContext(Ctx);

function strip(s: string) {
  return s.normalize("NFD").replace(/[\u0300-\u036f]/g, "").replace(/đ/g, "d").toLowerCase();
}

export function CommandPaletteProvider({ children }: { children: React.ReactNode }) {
  const [open, setOpen] = React.useState(false);
  const [q, setQ] = React.useState("");
  const router = useRouter();
  const emergency = useEmergency();
  const { mutate } = useSWRConfig();
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

  const m = strip(q).match(/^(wake|sleep|danh thuc|ngu)\s*(.*)$/);
  const verb = m ? (m[1] === "wake" || m[1] === "danh thuc" ? "wake" : "sleep") : null;
  const term = m ? m[2].trim() : "";
  const candidates = verb
    ? (boxes?.items ?? []).filter((b) => (verb === "wake" ? ["sleeping", "failed"] : ["running"]).includes(b.status) && b.slug.includes(term))
    : [];

  const act = async (b: Box, v: "wake" | "sleep") => {
    close();
    try {
      await (v === "wake" ? api.wakeBox(b.id) : api.sleepBox(b.id));
      toast.success(v === "wake" ? `Đang đánh thức ${b.slug}` : `Đã cho ${b.slug} ngủ`);
      mutate((k) => typeof k === "string" && k.startsWith("/boxes"));
      mutate("/overview");
    } catch (e) {
      toast.error(errMsg(e));
    }
  };

  const item = "flex cursor-pointer items-center gap-2.5 rounded-md px-2 py-2 text-sm aria-disabled:opacity-50 [&_svg]:size-4 [&_svg]:text-muted-foreground";

  return (
    <Ctx.Provider value={{ open: () => setOpen(true) }}>
      {children}
      <DialogPrimitive.Root open={open} onOpenChange={(o) => (o ? setOpen(true) : close())}>
        <DialogPrimitive.Portal>
          <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/60" />
          <DialogPrimitive.Content className="fixed left-1/2 top-[12dvh] z-50 w-[calc(100%-1.5rem)] max-w-xl -translate-x-1/2 overflow-hidden rounded-lg border border-border bg-popover shadow-2xl">
            <DialogPrimitive.Title className="sr-only">Bảng lệnh</DialogPrimitive.Title>
            <Command label="Bảng lệnh" shouldFilter={!verb} filter={(value, search) => (strip(value).includes(strip(search)) ? 1 : 0)}>
              <div className="flex items-center gap-2 border-b border-border px-3">
                <Search className="size-4 text-muted-foreground" />
                <Command.Input
                  value={q}
                  onValueChange={setQ}
                  autoFocus
                  placeholder="Gõ lệnh: wake shopee, sleep kecon, kill all, cost hôm nay…"
                  className="h-12 w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground"
                />
                <kbd className="hidden rounded border border-border px-1.5 font-mono text-[10px] text-muted-foreground sm:block">ESC</kbd>
              </div>
              <Command.List className="max-h-[60dvh] overflow-y-auto p-1.5">
                <Command.Empty className="px-3 py-6 text-center text-sm text-muted-foreground">
                  {verb ? `Không có box nào để ${verb === "wake" ? "đánh thức" : "cho ngủ"} khớp “${term}”` : "Không có lệnh phù hợp"}
                </Command.Empty>
                {verb ? (
                  <Command.Group heading={verb === "wake" ? "Đánh thức box" : "Cho box ngủ"}>
                    {candidates.map((b) => (
                      <Command.Item key={b.id} value={`${verb} ${b.slug}`} onSelect={() => act(b, verb)} className={item}>
                        {verb === "wake" ? <Sun /> : <Moon />}
                        <span className="font-mono">{b.slug}</span>
                        <span className="ml-auto"><BoxStatusChip status={b.status} /></span>
                      </Command.Item>
                    ))}
                  </Command.Group>
                ) : (
                  <>
                    <Command.Group heading="Lệnh nhanh">
                      <Command.Item value="cost hôm nay chi phí" className={item} onSelect={() => {
                        close();
                        if (ov) toast.info(`Hôm nay đã chi ${usd(ov.spent_today)} / ${usd(ov.budget_day)} ngân sách ngày`);
                        router.push("/costs");
                      }}>
                        <Wallet /> <span className="font-mono">cost hôm nay</span>
                        {ov && <span className="ml-auto font-mono text-xs text-muted-foreground">{usd(ov.spent_today)} / {usd(ov.budget_day)}</span>}
                      </Command.Item>
                      <Command.Item value="wake đánh thức box" className={item} onSelect={() => setQ("wake ")}>
                        <Sun /> <span className="font-mono">wake &lt;slug&gt;</span>
                        <span className="ml-auto text-xs text-muted-foreground">đánh thức box</span>
                      </Command.Item>
                      <Command.Item value="sleep ngủ box" className={item} onSelect={() => setQ("sleep ")}>
                        <Moon /> <span className="font-mono">sleep &lt;slug&gt;</span>
                        <span className="ml-auto text-xs text-muted-foreground">cho box ngủ</span>
                      </Command.Item>
                      <Command.Item value="kill all dừng khẩn cấp" className={`${item} text-st-error [&_svg]:text-st-error`} onSelect={() => { close(); emergency.open(); }}>
                        <OctagonX /> <span className="font-mono">kill all</span>
                        <span className="ml-auto text-xs">Dừng khẩn cấp</span>
                      </Command.Item>
                    </Command.Group>
                    <Command.Group heading="Đi tới">
                      {NAV.map((n) => (
                        <Command.Item key={n.href} value={`đi tới ${n.label} ${n.href}`} className={item} onSelect={() => { close(); router.push(n.href); }}>
                          <n.icon /> {n.label}
                        </Command.Item>
                      ))}
                      <Command.Item value="onboarding khởi động lần đầu" className={item} onSelect={() => { close(); router.push("/onboarding"); }}>
                        <Search /> Khởi động lần đầu
                      </Command.Item>
                    </Command.Group>
                    {(boxes?.items.length ?? 0) > 0 && (
                      <Command.Group heading="Boxes">
                        {boxes!.items.slice(0, 12).map((b) => (
                          <Command.Item key={b.id} value={`box ${b.slug} ${b.agent_name}`} className={item} onSelect={() => { close(); router.push(`/boxes/${b.id}`); }}>
                            <span className="font-mono">{b.slug}</span>
                            <span className="text-xs text-muted-foreground">{b.agent_name}</span>
                            <span className="ml-auto"><BoxStatusChip status={b.status} /></span>
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
