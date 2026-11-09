// Three goals at a glance, Apple Watch style: today's CI passes vs target, agents done vs started, inbox zero…
import { ActivityRings, Attachment, Column, Row, Text } from "@message-ui/components";
import { T, card } from "../theme";

type Ring = { label: string; current: number; goal: number; unit?: string };
type Props = { title: string; subtitle?: string; rings: [Ring, Ring, Ring] };

// ActivityRings draws its rings outer to inner in Move, Exercise, Stand colours (red, green, blue)
const COLORS = ["#fa114f", "#92e82a", "#1eeaef"];

export default function RingsCard({ title, subtitle, rings }: Props) {
  const [move, exercise, stand] = rings;
  return (
    <Attachment style={card}>
      <Text style={{ fontSize: 18, color: T.text, fontWeight: 700 }}>{title}</Text>
      {subtitle ? <Text style={{ fontSize: 13, color: T.muted, marginTop: 2 }}>{subtitle}</Text> : null}
      <Row style={{ marginTop: 16, alignItems: "center" }}>
        <ActivityRings move={move} exercise={exercise} stand={stand} size={150} />
        <Column style={{ marginLeft: 22, flex: 1 }}>
          {rings.map((r, i) => (
            <Column key={r.label} style={{ marginBottom: 10, flex: "none" }}>
              <Text style={{ fontSize: 13, color: T.muted }}>{r.label}</Text>
              <Text style={{ fontSize: 20, fontWeight: 700, color: COLORS[i] }}>{`${r.current}/${r.goal}${r.unit ?? ""}`}</Text>
            </Column>
          ))}
        </Column>
      </Row>
    </Attachment>
  );
}

RingsCard.PreviewProps = {
  title: "Today",
  subtitle: "Monday, 6 Oct",
  rings: [
    { label: "CI runs passed", current: 34, goal: 40 },
    { label: "Agent tasks done", current: 6, goal: 8 },
    { label: "Inbox handled", current: 18, goal: 20 },
  ],
} satisfies Props;
RingsCard.PreviewSize = { width: 400, height: 270 };
