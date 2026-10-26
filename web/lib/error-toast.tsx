"use client";
// v1.3: toast lỗi hiện hint + error.next (các lệnh gợi ý tiếp theo từ Core) để người dùng/agent biết làm gì tiếp.
import { toast } from "sonner";
import { useTranslations } from "next-intl";
import { ApiError } from "./api";

function ErrorDetails({ hint, next, code }: { hint?: string; next: string[]; code?: string }) {
  const t = useTranslations("apiErrors");
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      {hint && <p className="break-words">{hint}</p>}
      {next.length > 0 && (
        <div data-error-next>
          <p className="text-[11.5px] font-medium opacity-80">{t("tryNext")}</p>
          <ul className="mt-0.5 flex flex-col gap-0.5">
            {next.slice(0, 3).map((n) => (
              <li key={n} className="break-all font-mono text-[11.5px] leading-4">{n}</li>
            ))}
          </ul>
        </div>
      )}
      {code && <p className="font-mono text-[10.5px] opacity-60">{code}</p>}
    </div>
  );
}

/** Hiện toast lỗi; ApiError có hint/next thì hiện thêm phần gợi ý. */
export function toastError(e: unknown) {
  if (e instanceof ApiError) {
    const hasMore = !!e.hint || e.next.length > 0;
    toast.error(e.message, hasMore ? { description: <ErrorDetails hint={e.hint} next={e.next} code={e.code} />, duration: e.next.length ? 12000 : 6000 } : undefined);
    return;
  }
  toast.error(e instanceof Error ? e.message : String(e));
}
