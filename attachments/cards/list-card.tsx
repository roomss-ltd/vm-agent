// A short status list: agents and what they're doing, jobs, CI workflows, today's meetings. One row each.
import { Attachment, Column, Divider, Row, Text } from "@message-ui/components";
import { T, card } from "../theme";

type Item = { title: string; detail?: string; value?: string; status?: "ok" | "warn" | "bad" | "busy" | "idle" };
type Props = { title: string; subtitle?: string; items: Item[] };

const DOT = { ok: T.ok, warn: T.warn, bad: T.bad, busy: T.busy, idle: T.faint };

export default function ListCard({ title, subtitle, items }: Props) {
  return (
    <Attachment style={card}>
      <Text style={{ fontSize: 18, color: T.text, fontWeight: 700 }}>{title}</Text>
      {subtitle ? <Text style={{ fontSize: 13, color: T.muted, marginTop: 2 }}>{subtitle}</Text> : null}
      <Column style={{ marginTop: 14, flex: "none" }}>
        {items.map((it, i) => (
          <Column key={it.title + i} style={{ flex: "none" }}>
            {i ? <Divider style={{ backgroundColor: T.panel, margin: "10px 0" }} /> : null}
            <Row style={{ alignItems: "center" }}>
              <div style={{ width: 9, height: 9, borderRadius: 5, backgroundColor: DOT[it.status ?? "idle"], marginRight: 12 }} />
              <Column style={{ flex: 1 }}>
                <Text style={{ fontSize: 15, color: T.text, fontWeight: 600 }}>{it.title}</Text>
                {it.detail ? <Text style={{ fontSize: 12.5, color: T.muted, marginTop: 2 }}>{it.detail}</Text> : null}
              </Column>
              {it.value ? <Text style={{ fontSize: 14, color: T.muted, marginLeft: 10 }}>{it.value}</Text> : null}
            </Row>
          </Column>
        ))}
      </Column>
    </Attachment>
  );
}

ListCard.PreviewProps = {
  title: "Agents",
  subtitle: "4 waiting · 2 working",
  items: [
    { title: "flaky-test-fix", detail: "box-dev:12 · fixing a race in test_scheduler", status: "busy", value: "12m" },
    { title: "Scheduler fleet", detail: "dev-1:10 · tests pass, waiting for you", status: "warn", value: "waiting" },
    { title: "Control panel", detail: "box-dev:0 · done", status: "ok", value: "done" },
    { title: "Search perf", detail: "dev-1:23 · 2 subagents", status: "busy", value: "41m" },
  ],
} satisfies Props;
ListCard.PreviewSize = { width: 400, height: 320 };
// the canvas grows with the list (render.tsx asks)
ListCard.sizeFor = (p: Props) => ({ width: 400, height: 86 + (p.subtitle ? 20 : 0) + p.items.reduce((h, it, i) => h + (it.detail ? 37 : 22) + (i ? 21 : 0), 0) });
