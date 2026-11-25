import { Link } from "react-router-dom";
import type { Node } from "../api/types";
import { backhaulLabel } from "../lib/format";
import { Bars, StatusMark, cx } from "./ui";

function strongest(sig: Record<string, number> | null | undefined): number | null {
  if (!sig) return null;
  const v = Object.values(sig);
  return v.length ? Math.max(...v) : null;
}

/** The mesh as an indented tree: the main unit, then each satellite under its parent. */
export function MeshTree({ nodes }: { nodes: Node[] }) {
  const byParent = new Map<string, Node[]>();
  const ids = new Set(nodes.map((n) => n.id));
  const roots: Node[] = [];
  for (const n of nodes) {
    if (n.master || !n.parent_id || !ids.has(n.parent_id)) roots.push(n);
    else byParent.set(n.parent_id, [...(byParent.get(n.parent_id) ?? []), n]);
  }
  roots.sort((a, b) => Number(b.master) - Number(a.master));
  // Device-reported parents can form a cycle; draw each node once, and
  // any node never reached from a root at the top level.
  const drawn = new Set<string>();
  const render = (n: Node, depth: number): React.ReactNode => {
    if (drawn.has(n.id)) return null;
    drawn.add(n.id);
    const sig = strongest(n.backhaul?.signal);
    return (
      <li key={n.id}>
        <Link
          to={`/nodes/${encodeURIComponent(n.id)}`}
          className={cx("flex items-center gap-3 rounded-md px-2 py-2 hover:bg-sea", depth > 0 && "border-l-2 border-line")}
          style={{ marginLeft: depth * 20 }}
        >
          <StatusMark online={n.online} subject={n.id} />
          <span className="min-w-0 flex-1">
            <span dir="auto" className="block truncate font-medium">
              {n.nickname || n.model}
            </span>
            <span className="block text-xs text-drift">
              {n.model} · {n.master ? "Main unit" : backhaulLabel(n.backhaul?.type)}
              {n.online ? "" : " · offline"}
            </span>
          </span>
          {sig !== null && !n.master && <Bars level={sig} label="Backhaul signal" />}
          <span className="tnum w-16 text-right text-sm text-drift" title="Clients">
            {n.clients} {n.clients === 1 ? "client" : "clients"}
          </span>
        </Link>
        {(byParent.get(n.id) ?? []).length > 0 && <ul>{byParent.get(n.id)!.map((c) => render(c, depth + 1))}</ul>}
      </li>
    );
  };
  const tree = roots.map((r) => render(r, 0));
  const stray = nodes.filter((n) => !drawn.has(n.id)).map((n) => render(n, 0));
  return (
    <ul className="-mx-2">
      {tree}
      {stray}
    </ul>
  );
}
