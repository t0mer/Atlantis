import { Link } from "react-router-dom";
import { useState } from "react";
import { useEvents } from "../api/hooks";
import type { DecoEvent } from "../api/types";
import { Empty, PageTitle, cx, selectClass } from "../components/ui";
import { eventText, eventTone } from "../lib/format";
import { useNames } from "../lib/names";

const kinds: [string, string][] = [
  ["", "All changes"],
  ["client.", "Devices"],
  ["node.", "Decos"],
  ["wan.", "Internet"],
  ["firmware.", "Firmware"],
];

function dayLabel(d: Date): string {
  const today = new Date();
  const y = new Date(today);
  y.setDate(today.getDate() - 1);
  if (d.toDateString() === today.toDateString()) return "Today";
  if (d.toDateString() === y.toDateString()) return "Yesterday";
  return d.toLocaleDateString([], { weekday: "long", day: "numeric", month: "long" });
}

function link(e: DecoEvent): string | null {
  if (e.type.startsWith("client.")) return `/clients/${encodeURIComponent(e.subject)}`;
  if (e.type.startsWith("node.") || e.type.startsWith("firmware.")) return `/nodes/${encodeURIComponent(e.subject)}`;
  return null;
}

export function Events() {
  const { data: events = [], isLoading } = useEvents();
  const name = useNames();
  const [kind, setKind] = useState("");
  const shown = events.filter((e) => e.type.startsWith(kind));
  const days: { label: string; items: DecoEvent[] }[] = [];
  for (const e of shown) {
    const label = dayLabel(new Date(e.ts));
    const last = days[days.length - 1];
    if (last?.label === label) last.items.push(e);
    else days.push({ label, items: [e] });
  }
  return (
    <>
      <PageTitle
        aside={
          <label className="text-sm">
            <span className="sr-only">Show</span>
            <select className={selectClass} value={kind} onChange={(e) => setKind(e.target.value)}>
              {kinds.map(([v, t]) => (
                <option key={v} value={v}>
                  {t}
                </option>
              ))}
            </select>
          </label>
        }
      >
        Events
      </PageTitle>
      {!isLoading && !shown.length && <Empty>No changes recorded yet. Atlantis logs devices joining and leaving, Decos going offline and internet outages.</Empty>}
      <div className="space-y-6">
        {days.map((d) => (
          <section key={d.label}>
            <h2 className="mb-2 text-sm font-semibold">{d.label}</h2>
            <ol className="relative ml-1.5 border-l border-line">
              {d.items.map((e) => {
                const to = link(e);
                const tone = eventTone(e.type);
                const text = <span>{eventText(e, name)}</span>;
                return (
                  <li key={e.id} className="relative flex gap-4 py-1.5 pl-5 text-sm">
                    <span
                      aria-hidden
                      className={cx(
                        "absolute top-3 -left-[5px] size-2.5 rounded-full border-2 bg-sea",
                        tone === "bad" ? "border-flare" : tone === "warn" ? "border-buoy" : "border-lagoon",
                      )}
                    />
                    <time className="tnum w-16 shrink-0 whitespace-nowrap text-drift">{new Date(e.ts).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}</time>
                    {to ? (
                      <Link to={to} className="hover:underline">
                        {text}
                      </Link>
                    ) : (
                      text
                    )}
                  </li>
                );
              })}
            </ol>
          </section>
        ))}
      </div>
    </>
  );
}
