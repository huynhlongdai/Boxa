"use client";
import { Bar, BarChart, CartesianGrid, Cell, Legend, Pie, PieChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { useTranslations } from "next-intl";
import { AGENT_COLORS } from "@/lib/utils";
import { useFmt } from "@/lib/format";

const axis = { stroke: "#5b6472", fontSize: 11, tickLine: false, axisLine: false } as const;

function TipBox({ active, payload, label, title }: { active?: boolean; payload?: { name: string; value: number; color: string }[]; label?: string; title?: (l: string) => string }) {
  const t = useTranslations("charts");
  const { usd } = useFmt();
  if (!active || !payload?.length) return null;
  const total = payload.reduce((a, p) => a + (p.value || 0), 0);
  return (
    <div className="rounded-md border border-border bg-popover px-3 py-2 text-xs shadow-xl">
      {label !== undefined && <p className="mb-1 font-medium">{title ? title(String(label)) : label}</p>}
      {payload.filter((p) => p.value).map((p) => (
        <div key={p.name} className="flex items-center justify-between gap-4">
          <span className="flex items-center gap-1.5"><span className="size-2 rounded-sm" style={{ background: p.color }} />{p.name}</span>
          <span className="font-mono">{usd(p.value)}</span>
        </div>
      ))}
      {payload.length > 1 && (
        <div className="mt-1 flex justify-between border-t border-border pt-1 font-medium">
          <span>{t("total")}</span>
          <span className="font-mono">{usd(total)}</span>
        </div>
      )}
    </div>
  );
}

/** Biểu đồ cột xếp chồng chi phí theo agent. */
export function StackedCostChart({ data, height = 240 }: { data: { date: string; by_agent: Record<string, number> }[]; height?: number }) {
  const t = useTranslations("charts");
  const { usd, shortDate } = useFmt();
  const agents = Array.from(new Set(data.flatMap((d) => Object.keys(d.by_agent)))).sort();
  const rows = data.map((d) => ({ date: d.date, ...d.by_agent }));
  return (
    <ResponsiveContainer width="100%" height={height}>
      <BarChart data={rows} margin={{ top: 8, right: 4, left: -8, bottom: 0 }}>
        <CartesianGrid stroke="#262b33" vertical={false} />
        <XAxis dataKey="date" tickFormatter={(v: string) => shortDate(v)} {...axis} />
        <YAxis tickFormatter={(v: number) => usd(v, 2)} width={60} {...axis} />
        <Tooltip cursor={{ fill: "rgba(255,255,255,0.04)" }} content={<TipBox title={(l) => t("day", { date: shortDate(l) })} />} />
        <Legend iconType="square" iconSize={8} wrapperStyle={{ fontSize: 11, paddingTop: 6 }} />
        {agents.map((a, i) => (
          <Bar key={a} dataKey={a} stackId="c" fill={AGENT_COLORS[i % AGENT_COLORS.length]} radius={i === agents.length - 1 ? [3, 3, 0, 0] : 0} maxBarSize={36} />
        ))}
      </BarChart>
    </ResponsiveContainer>
  );
}

export function HBarChart({ data, height = 200 }: { data: { name: string; value: number }[]; height?: number }) {
  const t = useTranslations("charts");
  const { usd } = useFmt();
  return (
    <ResponsiveContainer width="100%" height={height}>
      <BarChart data={data} layout="vertical" margin={{ top: 0, right: 12, left: 8, bottom: 0 }}>
        <CartesianGrid stroke="#262b33" horizontal={false} />
        <XAxis type="number" tickFormatter={(v: number) => usd(v, 2)} {...axis} />
        <YAxis type="category" dataKey="name" width={84} {...axis} />
        <Tooltip cursor={{ fill: "rgba(255,255,255,0.04)" }} content={<TipBox />} />
        <Bar dataKey="value" name={t("cost")} radius={[0, 3, 3, 0]} maxBarSize={22}>
          {data.map((_, i) => (
            <Cell key={i} fill={AGENT_COLORS[i % AGENT_COLORS.length]} />
          ))}
        </Bar>
      </BarChart>
    </ResponsiveContainer>
  );
}

export function DonutChart({ data, height = 200 }: { data: { name: string; value: number; color: string }[]; height?: number }) {
  return (
    <ResponsiveContainer width="100%" height={height}>
      <PieChart>
        <Pie data={data} dataKey="value" nameKey="name" innerRadius="58%" outerRadius="85%" paddingAngle={2} stroke="none">
          {data.map((d) => (
            <Cell key={d.name} fill={d.color} />
          ))}
        </Pie>
        <Tooltip content={<TipBox />} />
        <Legend iconType="circle" iconSize={8} wrapperStyle={{ fontSize: 11 }} />
      </PieChart>
    </ResponsiveContainer>
  );
}
