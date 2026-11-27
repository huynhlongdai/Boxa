import { RequirePerm } from "@/components/boxmes/workspace";

/** v2.0: khu Nền tảng — chỉ quản trị nền tảng (Core cũng từ chối người khác). */
export default function PlatformLayout({ children }: { children: React.ReactNode }) {
  return <RequirePerm platform>{children}</RequirePerm>;
}
