"use client";
import { LiveProvider } from "./live";
import { EmergencyProvider } from "./boxmes/emergency";
import { CommandPaletteProvider } from "./command-palette";

export function Providers({ children }: { children: React.ReactNode }) {
  return (
    <LiveProvider>
      <EmergencyProvider>
        <CommandPaletteProvider>{children}</CommandPaletteProvider>
      </EmergencyProvider>
    </LiveProvider>
  );
}
