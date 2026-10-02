// board/evidence/s3-mermaid.mjs -- the committed, reproducible evidence for the
// Mermaid slice (S3): boot a DOM, parse the fixture flowchart with the pinned
// @excalidraw/mermaid-to-excalidraw 1.1.2, recolour the converter's element
// skeletons by role from the theme the page would send, and assert the roles.
//
// Usage:
//   node board/evidence/s3-mermaid.mjs [theme.json]
//
// With no argument the script uses a fixed sentinel theme, so the run is
// self-contained and still catches a recolour that stopped working. When given
// a theme JSON path -- the `theme` object of the running page's own
// `GET /api/scene` (or the whole body) -- it uses that palette and prints the
// final colours too, so no product palette value is duplicated in board/.
//
// The converter needs a DOM (mermaid.render touches document), so boot jsdom
// and stub the two SVG measurement calls mermaid makes. It cannot get past the
// converter's skeletons: @excalidraw/excalidraw itself cannot be imported under
// node (`ERR_MODULE_NOT_FOUND ... roughjs/bin/rough`), and the browser check
// (S3.8) covers everything after the skeletons. This script therefore calls
// parseMermaidToExcalidraw + recolor directly, the two steps convert runs before
// it reaches the Excalidraw import.

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import { JSDOM } from "jsdom";

const here = dirname(fileURLToPath(import.meta.url));

// A sentinel palette, used when no theme JSON is given. It is not the product
// palette: it only has to be distinguishable per role, and it keeps this script
// runnable without a running server.
const SENTINEL_THEME = {
  bg: "#101112",
  panel: "#202122",
  ink: "#303132",
  line: "#404142",
  faint: "#505152",
};

// expected turns a theme into the stroke/fill recolor must write per role, the
// role table from the plan. "other" is deliberately left to the converter.
function expected(role, theme) {
  switch (role) {
    case "node":
      return { strokeColor: theme.ink, backgroundColor: theme.panel };
    case "edge":
      return { strokeColor: theme.line, backgroundColor: "transparent" };
    case "cluster":
      return { strokeColor: theme.line, backgroundColor: theme.faint };
    default:
      return null;
  }
}

function fail(message) {
  console.error("s3 evidence: " + message);
  process.exit(1);
}

// --- boot a DOM -----------------------------------------------------------------
const dom = new JSDOM("<!doctype html><html><body></body></html>", { pretendToBeVisual: true });
globalThis.window = dom.window;
globalThis.document = dom.window.document;
Object.defineProperty(globalThis, "navigator", { value: dom.window.navigator, configurable: true });

const svgProto = dom.window.SVGElement.prototype;
svgProto.getBBox = () => ({ x: 0, y: 0, width: 100, height: 20 });
svgProto.getComputedTextLength = () => 100;

// --- theme ----------------------------------------------------------------------
const themePath = process.argv[2];
const fromJson = themePath ? JSON.parse(readFileSync(themePath, "utf8")) : null;
const theme = fromJson ? fromJson.theme || fromJson : SENTINEL_THEME;

const { parseMermaidToExcalidraw } = await import("@excalidraw/mermaid-to-excalidraw");
const { roleOf, recolor, themeVariables } = await import("../src/mermaid.js");

const fixture = readFileSync(join(here, "s3-mermaid.txt"), "utf8");

const parsed = await parseMermaidToExcalidraw(fixture, {
  theme: "base",
  themeVariables: themeVariables(theme),
});
const descriptors = parsed.elements;
const recolored = recolor(descriptors, descriptors, theme);

// --- one line per skeleton ------------------------------------------------------
const lines = [];
for (let i = 0; i < recolored.length; i++) {
  const element = recolored[i];
  const role = roleOf(element, descriptors);
  lines.push(
    [role, element.id, JSON.stringify(element.groupIds || []), (element.label && element.label.text) || "", element.strokeColor, element.backgroundColor].join("\t"),
  );

  if (element.id !== descriptors[i].id) {
    fail(`skeleton ${i} (${element.id}) does not match descriptor ${descriptors[i].id}; the converter's order changed`);
  }

  const want = expected(role, theme);
  if (want) {
    if (element.strokeColor !== want.strokeColor) {
      fail(`${role} skeleton ${element.id} has strokeColor ${element.strokeColor}, expected ${want.strokeColor}; the recolour did not run`);
    }
    if (element.backgroundColor !== want.backgroundColor) {
      fail(`${role} skeleton ${element.id} has backgroundColor ${element.backgroundColor}, expected ${want.backgroundColor}; the recolour did not run`);
    }
  }
}

// --- role counts and no image fallback ------------------------------------------
const counts = { cluster: 0, node: 0, edge: 0, other: 0 };
for (const element of recolored) {
  counts[roleOf(element, descriptors)] += 1;
}
const fileCount = parsed.files ? Object.keys(parsed.files).length : 0;

if (counts.node !== 4 || counts.cluster !== 1 || counts.edge !== 3) {
  fail(`expected 4 nodes / 1 cluster / 3 edges, got ${counts.node} nodes / ${counts.cluster} cluster / ${counts.edge} edges`);
}
if (fileCount !== 0) {
  fail(`expected 0 files, got ${fileCount}`);
}

process.stdout.write(lines.join("\n") + "\n");
if (fromJson) {
  const final = {};
  for (const role of ["cluster", "node", "edge"]) {
    const element = recolored.find((candidate) => roleOf(candidate, descriptors) === role);
    final[role] = { strokeColor: element.strokeColor, backgroundColor: element.backgroundColor };
  }
  process.stdout.write("final colours: " + JSON.stringify(final) + "\n");
}
process.stdout.write("s3 evidence: ok\n");