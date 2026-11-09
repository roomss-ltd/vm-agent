// Render a message-ui card to a PNG with your data, for the chat (an inline image in the panel, an attachment on
// Telegram).
//   npx tsx render.tsx <card> [--props '<json>' | --props @file.json] [--width N] [--height N] [--out file.png]
//   npx tsx render.tsx --list        the cards, what each is for and its sample props, as JSON
//   npx tsx render.tsx <card> --html [--props …]   the card as HTML on stdout: the control panel shows it live
// <card> is a name in cards/ (metric-card) or a path to any .tsx template (default export + PreviewSize). Props
// are merged over the card's PreviewProps. Prints the PNG's path. Preview them all live: npm run dev.
import { renderToPng } from "@message-ui/render";
import { createElement } from "react";
import { existsSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { basename, dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const args = process.argv.slice(2);
const opt = (k: string) => { const i = args.indexOf("--" + k); return i >= 0 ? args[i + 1] : undefined; };
const load = async (p: string) => (await import(pathToFileURL(p).href)).default;

if (args[0] === "--list") {
  const out = [];
  for (const f of readdirSync(join(here, "cards")).filter((f) => f.endsWith(".tsx")).sort()) {
    const C = await load(join(here, "cards", f));
    const about = readFileSync(join(here, "cards", f), "utf8").split("\n")[0].replace(/^\/\/\s*/, "");
    out.push({ card: f.replace(/\.tsx$/, ""), about, size: C.PreviewSize, sampleProps: C.PreviewProps });
  }
  console.log(JSON.stringify(out, null, 2));
  process.exit(0);
}

const name = args[0];
if (!name) { console.error("usage: npx tsx render.tsx <card> [--props json] [--width N] [--height N] [--out file.png]"); process.exit(2); }
const file = existsSync(join(here, "cards", name + ".tsx")) ? join(here, "cards", name + ".tsx") : resolve(name);
const C = await load(file);
if (typeof C !== "function") { console.error(`${file}: no default export component`); process.exit(2); }
let raw = opt("props") ?? "{}";
if (raw.startsWith("@")) raw = readFileSync(raw.slice(1), "utf8");
const props = { ...(C.PreviewProps ?? {}), ...JSON.parse(raw) };
const size = { width: 400, height: 400, ...(C.PreviewSize ?? {}), ...(C.sizeFor?.(props) ?? {}) };
if (opt("width")) size.width = +opt("width")!;
if (opt("height")) size.height = +opt("height")!;
if (args.includes("--html")) {
  const { renderToStaticMarkup } = await import("react-dom/server");
  process.stdout.write(`<div class="mui-card" style="width:${size.width}px">${renderToStaticMarkup(createElement(C, props))}</div>`);
  process.exit(0);
}
const png = await renderToPng(createElement(C, props), { ...size, scale: 3 });
const stamp = new Date().toISOString().replace(/[-:]/g, "").replace("T", "-").slice(0, 15);
const out = resolve(opt("out") ?? join(homedir(), "jarvis-files", "cards", `${basename(file, ".tsx")}-${stamp}.png`));
mkdirSync(dirname(out), { recursive: true });
writeFileSync(out, png);
console.log(out);
