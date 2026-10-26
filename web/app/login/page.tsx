import { Suspense } from "react";
import { LoginForm } from "./login-form";

export const metadata = { title: "Đăng nhập" };

export default function LoginPage() {
  return (
    <div className="grid min-h-dvh place-items-center px-4">
      <Suspense>
        <LoginForm />
      </Suspense>
    </div>
  );
}
