import Link from "next/link";

export default function NotFound() {
  return (
    <div className="grid min-h-dvh place-items-center px-4 text-center">
      <div>
        <p className="font-mono text-sm text-muted-foreground">404</p>
        <h1 className="mt-2 text-xl font-semibold">Không tìm thấy trang này</h1>
        <Link href="/" className="mt-4 inline-block text-sm text-primary hover:underline">Về Tổng quan</Link>
      </div>
    </div>
  );
}
