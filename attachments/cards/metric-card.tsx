// A headline number with its trend: CPU, spend, CI pass rate, requests… One big value, how it moved, a line chart.
import { Attachment, Heading, LineChart, Row, Text } from "@message-ui/components";
import { T, card } from "../theme";

type Props = {
  title: string;
  value: string;
  delta?: string; // "+12%", "-3", shown green when it starts with +, red with - (flip with goodWhenDown)
  caption?: string; // "vs last week"
  series: number[];
  labels?: string[];
  color?: string;
  goodWhenDown?: boolean;
};

export default function MetricCard({ title, value, delta, caption, series, labels, color = T.palette[0], goodWhenDown }: Props) {
  const up = delta?.trim().startsWith("+");
  const good = delta ? (goodWhenDown ? !up : up) : true;
  return (
    <Attachment style={card}>
      <Text style={{ fontSize: 14, color: T.muted, fontWeight: 500 }}>{title}</Text>
      <Row style={{ alignItems: "flex-end", marginTop: 6 }}>
        <Heading level={1} style={{ color: T.text, fontSize: 40, lineHeight: 1 }}>{value}</Heading>
        {delta ? (
          <Text style={{ marginLeft: 10, marginBottom: 4, fontSize: 15, fontWeight: 600, color: good ? T.ok : T.bad }}>{delta}</Text>
        ) : null}
      </Row>
      {caption ? <Text style={{ fontSize: 13, color: T.faint, marginTop: 4 }}>{caption}</Text> : null}
      <LineChart
        series={series}
        labels={labels}
        width={352}
        height={130}
        color={color}
        strokeWidth={3}
        gridColor="rgba(255,255,255,0.06)"
        areaColor={color + "33"}
        style={{ marginTop: 18 }}
      />
    </Attachment>
  );
}

MetricCard.PreviewProps = {
  title: "CI pass rate",
  value: "84%",
  delta: "+6%",
  caption: "last 7 days vs the week before",
  series: [71, 80, 64, 90, 88, 75, 84],
  labels: ["Tue", "Wed", "Thu", "Fri", "Sat", "Sun", "Mon"],
} satisfies Props;
MetricCard.PreviewSize = { width: 400, height: 330 };
