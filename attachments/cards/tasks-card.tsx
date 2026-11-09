// A few tasks at a glance: each with its status, progress bar and where it stands.
import { Attachment, Column, Divider, Row, Text } from "@message-ui/components";
import { T, card } from "../theme";

type Task = { id: number; title: string; status: "in_progress" | "blocked" | "done"; progress: number; note?: string };
type Props = { title: string; subtitle?: string; tasks: Task[] };

const COLOR = { in_progress: T.busy, blocked: T.warn, done: T.ok };
const LABEL = { in_progress: "In progress", blocked: "Blocked", done: "Done" };

export default function TasksCard({ title, subtitle, tasks }: Props) {
  return (
    <Attachment style={card}>
      <Text style={{ fontSize: 18, color: T.text, fontWeight: 700 }}>{title}</Text>
      {subtitle ? <Text style={{ fontSize: 13, color: T.muted, marginTop: 2 }}>{subtitle}</Text> : null}
      <Column style={{ marginTop: 14, flex: "none" }}>
        {tasks.map((t, i) => (
          <Column key={t.id} style={{ flex: "none" }}>
            {i ? <Divider style={{ backgroundColor: T.panel, margin: "11px 0" }} /> : null}
            <Row style={{ alignItems: "center" }}>
              <div style={{ width: 9, height: 9, borderRadius: 5, backgroundColor: COLOR[t.status], marginRight: 10 }} />
              <Text style={{ flex: 1, fontSize: 14.5, color: T.text, fontWeight: 600 }}>{`#${t.id} ${t.title}`}</Text>
              <Text style={{ fontSize: 13, color: COLOR[t.status], fontWeight: 700, marginLeft: 8 }}>{`${t.progress}%`}</Text>
            </Row>
            <div style={{ display: "flex", height: 5, borderRadius: 3, backgroundColor: T.panel, marginTop: 7 }}>
              <div style={{ width: `${Math.max(3, Math.min(100, t.progress))}%`, height: 5, borderRadius: 3, backgroundColor: COLOR[t.status] }} />
            </div>
            <Text style={{ fontSize: 12, color: T.muted, marginTop: 6 }}>
              {`${LABEL[t.status]}${t.note ? ` · ${t.note}` : ""}`}
            </Text>
          </Column>
        ))}
      </Column>
    </Attachment>
  );
}

TasksCard.PreviewProps = {
  title: "Tasks",
  subtitle: "1 in progress · 2 waiting on you",
  tasks: [
    { id: 4, title: "Search page performance", status: "in_progress", progress: 85, note: "finishing the last benchmark runs" },
    { id: 5, title: "Nightly job cost", status: "blocked", progress: 35, note: "needs your yes on the cost cuts" },
    { id: 2, title: "Booking page on our domain", status: "blocked", progress: 30, note: "verify the domain with the provider" },
  ],
} satisfies Props;
TasksCard.PreviewSize = { width: 420, height: 330 };
// the canvas grows with the list; a note wraps to a second line past ~52 characters
TasksCard.sizeFor = (p: Props) => ({
  width: 420,
  height: 62 + (p.subtitle ? 20 : 0) + p.tasks.reduce((h, t, i) => h + 58 + ((t.note?.length ?? 0) > 52 ? 16 : 0) + (i ? 23 : 0), 0),
});
