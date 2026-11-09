// How a whole splits into parts: memory by process, spend by service, failures by cause. A donut and its legend.
import { Attachment, DonutChart, Row, Column, Text } from "@message-ui/components";
import { T, card } from "../theme";

type Props = {
  title: string;
  subtitle?: string;
  center?: string; // in the ring, e.g. "17 GB"
  centerSub?: string; // under it, e.g. "used"
  unit?: string; // after each legend value, e.g. " GB"
  segments: { label: string; value: number; color?: string }[];
};

export default function BreakdownCard({ title, subtitle, center, centerSub, unit = "", segments }: Props) {
  const total = segments.reduce((s, x) => s + x.value, 0) || 1;
  const segs = segments.map((s, i) => ({ ...s, color: s.color || T.palette[i % T.palette.length] }));
  return (
    <Attachment style={card}>
      <Text style={{ fontSize: 18, color: T.text, fontWeight: 700 }}>{title}</Text>
      {subtitle ? <Text style={{ fontSize: 13, color: T.muted, marginTop: 2 }}>{subtitle}</Text> : null}
      <Row style={{ marginTop: 18, alignItems: "center" }}>
        <DonutChart segments={segs} size={150} strokeWidth={18} trackColor={T.panel} centerLabel={center} centerSublabel={centerSub} />
        <Column style={{ marginLeft: 22, flex: 1 }}>
          {segs.map((s) => (
            <Row key={s.label} style={{ alignItems: "center", marginBottom: 9 }}>
              <div style={{ width: 10, height: 10, borderRadius: 5, backgroundColor: s.color, marginRight: 8 }} />
              <Text style={{ fontSize: 13, color: T.text, flex: 1 }}>{s.label}</Text>
              <Text style={{ fontSize: 13, color: T.muted, marginLeft: 8 }}>{`${s.value}${unit} · ${Math.round((s.value / total) * 100)}%`}</Text>
            </Row>
          ))}
        </Column>
      </Row>
    </Attachment>
  );
}

BreakdownCard.PreviewProps = {
  title: "Memory by process",
  subtitle: "17.3 of 32 GB in use",
  center: "17.3",
  centerSub: "GB used",
  unit: " GB",
  segments: [
    { label: "docker", value: 8.2 },
    { label: "claude", value: 3.2 },
    { label: "node", value: 1.4 },
    { label: "chrome", value: 0.9 },
    { label: "runners", value: 0.7 },
  ],
} satisfies Props;
BreakdownCard.PreviewSize = { width: 400, height: 250 };
