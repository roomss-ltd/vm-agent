// The cards' look: message-ui's own (zinc, Inter), with the panel's accents for data.
export const T = {
  bg: "#18181b",
  panel: "#27272a",
  border: "#3f3f46",
  text: "#fafafa",
  muted: "#a1a1aa",
  faint: "#71717a",
  ok: "#4ade80",
  warn: "#fbbf24",
  bad: "#f87171",
  busy: "#60a5fa",
  palette: ["#7aa2ff", "#2dd4bf", "#bc8cff", "#34d399", "#f472b6", "#fbbf24", "#60a5fa", "#fb7185"],
};
export const card = { padding: 24, backgroundColor: T.bg, borderRadius: 24, width: "100%", height: "100%" } as const;
