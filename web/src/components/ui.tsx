import * as RDialog from "@radix-ui/react-dialog";
import * as RAlert from "@radix-ui/react-alert-dialog";
import * as RSwitch from "@radix-ui/react-switch";
import { X } from "lucide-react";
import { createContext, useCallback, useContext, useState, type ReactNode } from "react";
import { useRecentlyChanged } from "../api/live";

const cx = (...c: (string | false | null | undefined)[]) => c.filter(Boolean).join(" ");

export function Panel({ title, action, children, className }: { title?: ReactNode; action?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={cx("rounded-lg border border-line bg-shoal", className)}>
      {(title || action) && (
        <header className="flex items-center justify-between gap-3 px-4 pt-3">
          {title && <h2 className="text-sm font-semibold">{title}</h2>}
          {action}
        </header>
      )}
      <div className="p-4">{children}</div>
    </section>
  );
}

export function PageTitle({ children, aside }: { children: ReactNode; aside?: ReactNode }) {
  return (
    <div className="mb-5 flex flex-wrap items-end justify-between gap-3">
      <h1 className="text-xl font-semibold tracking-tight">{children}</h1>
      {aside}
    </div>
  );
}

type Tone = "primary" | "quiet" | "danger";
export function Button({
  tone = "quiet",
  className,
  ...p
}: React.ButtonHTMLAttributes<HTMLButtonElement> & { tone?: Tone }) {
  return (
    <button
      {...p}
      className={cx(
        "inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-sm font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50",
        tone === "primary" && "bg-lagoon text-shoal hover:brightness-110",
        tone === "quiet" && "border border-line bg-shoal hover:bg-sea",
        tone === "danger" && "bg-flare text-shoal hover:brightness-110",
        className,
      )}
    />
  );
}

export function Field({ label, hint, children }: { label: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <label className="block space-y-1">
      <span className="text-sm font-medium">{label}</span>
      {children}
      {hint && <span className="block text-xs text-drift">{hint}</span>}
    </label>
  );
}

export const inputClass =
  "w-full rounded-md border border-line bg-shoal px-2.5 py-1.5 text-sm outline-none placeholder:text-drift focus:border-lagoon";

export const selectClass =
  "rounded-md border border-line bg-shoal px-2.5 py-1.5 text-sm outline-none focus:border-lagoon";

export function Input(p: React.InputHTMLAttributes<HTMLInputElement>) {
  return <input {...p} className={cx(inputClass, p.className)} />;
}

export function Switch({ checked, onChange, label, disabled }: { checked: boolean; onChange?: (v: boolean) => void; label: string; disabled?: boolean }) {
  return (
    <RSwitch.Root
      checked={checked}
      onCheckedChange={onChange}
      disabled={disabled}
      aria-label={label}
      className="relative h-5 w-9 shrink-0 rounded-full border border-line bg-sea transition-colors data-[state=checked]:border-lagoon data-[state=checked]:bg-lagoon disabled:opacity-50"
    >
      <RSwitch.Thumb className="block size-3.5 translate-x-0.5 rounded-full bg-shoal shadow-sm transition-transform data-[state=checked]:translate-x-[18px]" />
    </RSwitch.Root>
  );
}

/** Online/offline marker: filled vs hollow, never colour alone. */
export function StatusMark({ online, subject, warn }: { online: boolean; subject?: string; warn?: boolean }) {
  const pulse = useRecentlyChanged(subject ?? "");
  return (
    <span
      aria-hidden
      className={cx(
        "inline-block size-2.5 shrink-0 rounded-full border-2",
        warn ? "border-buoy bg-buoy" : online ? "border-lagoon bg-lagoon" : "border-drift bg-transparent",
        pulse && "pulse-once",
      )}
    />
  );
}

export function sessionDot(tone: "ok" | "warn" | "bad") {
  return <span aria-hidden className={cx("size-2 shrink-0 rounded-full", tone === "ok" ? "bg-lagoon" : tone === "warn" ? "bg-buoy" : "bg-flare")} />;
}

export function OnlineLabel({ online, subject }: { online: boolean; subject?: string }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <StatusMark online={online} subject={subject} />
      <span className={online ? "" : "text-drift"}>{online ? "Online" : "Offline"}</span>
    </span>
  );
}

/** Signal in bars (0-4), drawn as four steps. */
export function Bars({ level, label = "Signal" }: { level: number; label?: string }) {
  return (
    <span role="img" aria-label={`${label} ${level} of 4`} className="inline-flex items-end gap-[2px]">
      {[1, 2, 3, 4].map((i) => (
        <span key={i} className={cx("w-[3px] rounded-sm", i <= level ? "bg-abyss" : "bg-line")} style={{ height: 3 + i * 3 }} />
      ))}
    </span>
  );
}

export function Dialog({ open, onOpenChange, title, children }: { open: boolean; onOpenChange: (v: boolean) => void; title: string; children: ReactNode }) {
  return (
    <RDialog.Root open={open} onOpenChange={onOpenChange}>
      <RDialog.Portal>
        <RDialog.Overlay className="fixed inset-0 bg-abyss/40" />
        <RDialog.Content className="fixed top-1/2 left-1/2 max-h-[90vh] w-[min(92vw,32rem)] -translate-x-1/2 -translate-y-1/2 overflow-y-auto rounded-lg border border-line bg-shoal p-5 shadow-xl">
          <div className="mb-4 flex items-start justify-between gap-4">
            <RDialog.Title className="text-lg font-semibold">{title}</RDialog.Title>
            <RDialog.Close aria-label="Close" className="rounded p-1 text-drift hover:text-abyss">
              <X size={18} />
            </RDialog.Close>
          </div>
          <RDialog.Description className="sr-only">{title}</RDialog.Description>
          {children}
        </RDialog.Content>
      </RDialog.Portal>
    </RDialog.Root>
  );
}

/**
 * A destructive or disruptive confirmation. When typed is set the person
 * must type it before the action is enabled.
 */
export function Confirm({
  open,
  onOpenChange,
  title,
  body,
  action,
  typed,
  onConfirm,
  busy,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  title: string;
  body: ReactNode;
  action: string;
  typed?: string;
  onConfirm: () => void;
  busy?: boolean;
}) {
  const [text, setText] = useState("");
  const ready = !typed || text === typed;
  return (
    <RAlert.Root open={open} onOpenChange={(v) => (setText(""), onOpenChange(v))}>
      <RAlert.Portal>
        <RAlert.Overlay className="fixed inset-0 bg-abyss/40" />
        <RAlert.Content className="fixed top-1/2 left-1/2 w-[min(92vw,28rem)] -translate-x-1/2 -translate-y-1/2 rounded-lg border border-line bg-shoal p-5 shadow-xl">
          <RAlert.Title className="text-lg font-semibold">{title}</RAlert.Title>
          <RAlert.Description asChild>
            <div className="mt-2 space-y-2 text-sm text-drift">{body}</div>
          </RAlert.Description>
          {typed && (
            <div className="mt-4">
              <Field label={`Type ${typed} to confirm`}>
                <Input value={text} onChange={(e) => setText(e.target.value)} autoComplete="off" dir="auto" />
              </Field>
            </div>
          )}
          <div className="mt-5 flex justify-end gap-2">
            <RAlert.Cancel asChild>
              <Button>Cancel</Button>
            </RAlert.Cancel>
            <Button tone="danger" disabled={!ready || busy} onClick={onConfirm}>
              {busy ? "Working…" : action}
            </Button>
          </div>
        </RAlert.Content>
      </RAlert.Portal>
    </RAlert.Root>
  );
}

// Toasts: one line confirming what happened, or what went wrong.
type ToastMsg = { id: number; text: string; tone: "ok" | "bad" };
const ToastCtx = createContext<(text: string, tone?: "ok" | "bad") => void>(() => {});
export const useToast = () => useContext(ToastCtx);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastMsg[]>([]);
  const push = useCallback((text: string, tone: "ok" | "bad" = "ok") => {
    const id = Date.now() + Math.random();
    setItems((x) => [...x, { id, text, tone }]);
    setTimeout(() => setItems((x) => x.filter((t) => t.id !== id)), 4500);
  }, []);
  return (
    <ToastCtx.Provider value={push}>
      {children}
      <div aria-live="polite" className="fixed right-4 bottom-20 z-50 space-y-2 md:bottom-4">
        {items.map((t) => (
          <div
            key={t.id}
            className={cx(
              "max-w-sm rounded-md border px-3 py-2 text-sm shadow-lg",
              t.tone === "ok" ? "border-lagoon bg-shoal" : "border-flare bg-shoal text-flare",
            )}
          >
            {t.text}
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return <p className="py-6 text-center text-sm text-drift">{children}</p>;
}

export function ErrorNote({ error }: { error: unknown }) {
  if (!error) return null;
  return <p className="rounded-md border border-flare px-3 py-2 text-sm text-flare">{(error as Error).message}</p>;
}

export { cx };
