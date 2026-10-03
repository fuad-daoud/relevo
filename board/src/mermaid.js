// The Mermaid surface of the board page: parse Mermaid text into element
// skeletons, recolour them by role from the theme the server sent, and turn
// them into the Excalidraw elements the canvas takes.
//
// The module is pure ESM. It imports @excalidraw/excalidraw lazily, inside
// convert: a static import makes this module unimportable under node
// (@excalidraw/excalidraw resolves `roughjs/bin/rough`, which has no node
// entry), and the committed evidence script runs under node and stops at the
// converter's skeletons. The browser bundle inlines the lazy import, so the
// page is unaffected.

import { parseMermaidToExcalidraw } from "@excalidraw/mermaid-to-excalidraw";

// themeVariables maps the server's theme object onto the Mermaid config keys
// the rendered SVG reads. mermaid honours these colour keys only under
// theme "base" (measured: with no theme they are absent from the SVG), so
// convert passes theme "base" beside them.
export function themeVariables(theme) {
  return {
    background: theme.bg,
    primaryColor: theme.panel,
    primaryBorderColor: theme.ink,
    primaryTextColor: theme.ink,
    mainBkg: theme.panel,
    nodeBorder: theme.ink,
    secondaryColor: theme.panel,
    tertiaryColor: theme.panel,
    lineColor: theme.line,
    textColor: theme.ink,
    clusterBkg: theme.faint,
    clusterBorder: theme.line,
    fontFamily: "Cascadia",
  };
}

// roleOf names the role one converter descriptor (an element skeleton) plays.
// The subgraph pass runs first and tags its own skeleton with
// "subgraph_group_<id>", which is the measured signature of a cluster; an
// absent id is the same thing. `descriptors` is the skeleton list, so a
// cluster is still found when only another descriptor carries its tag.
export function roleOf(element, descriptors) {
  const tag = "subgraph_group_" + element.id;
  const carries = (groups) => (groups || []).indexOf(tag) >= 0;
  if (
    element.id === undefined ||
    element.id === null ||
    carries(element.groupIds) ||
    (descriptors || []).some((descriptor) => carries(descriptor.groupIds))
  ) {
    return "cluster";
  }
  if (element.type === "arrow" || element.type === "line") {
    return "edge";
  }
  if (element.type === "rectangle" || element.type === "ellipse" || element.type === "diamond") {
    return "node";
  }
  return "other";
}

// recolor returns a new array of shallow copies, each carrying the theme's
// colours for its role: nodes take panel/ink, edges take line, clusters take
// faint as a fill with line as their outline, and "other" keeps the
// converter's colours. The input is never mutated.
export function recolor(elements, descriptors, theme) {
  return elements.map((element) => {
    const role = roleOf(element, descriptors);
    const copy = Object.assign({}, element);
    switch (role) {
      case "node":
        copy.strokeColor = theme.ink;
        copy.backgroundColor = theme.panel;
        copy.fillStyle = "solid";
        break;
      case "edge":
        copy.strokeColor = theme.line;
        copy.backgroundColor = "transparent";
        break;
      case "cluster":
        copy.strokeColor = theme.line;
        copy.backgroundColor = theme.faint;
        copy.fillStyle = "solid";
        break;
      default:
        copy.strokeColor = element.strokeColor;
        copy.backgroundColor = element.backgroundColor;
        break;
    }
    return copy;
  });
}

// convert parses `text`, recolours the converter's skeletons by role from
// `theme`, and returns the Excalidraw elements the canvas takes, the files the
// diagram carries (the unsupported-diagram image fallback only), and the
// recoloured skeletons the preview lists. The skeletons keep the Mermaid names
// (A, B, A_B) as their ids; convertToExcalidrawElements regenerates every id
// on the elements it returns, so two imports cannot collide.
export async function convert(text, theme) {
  try {
    const parsed = await parseMermaidToExcalidraw(text, {
      // @excalidraw/mermaid-to-excalidraw 1.1.2's MermaidConfig type names only
      // themeVariables.fontSize, but its dist/parseMermaid.js spreads the whole
      // config into mermaid.initialize, so `theme` and the colour keys reach
      // mermaid. A config the type does not name is intentional.
      theme: "base",
      themeVariables: themeVariables(theme),
    });
    const skeletons = recolor(parsed.elements, parsed.elements, theme);
    const { convertToExcalidrawElements } = await import("@excalidraw/excalidraw");
    const elements = convertToExcalidrawElements(skeletons);
    return { elements, files: parsed.files, skeletons };
  } finally {
    // parseMermaid appends a hidden #mermaid-diagram node to the body and
    // removes it only on success, so a failed render leaves one behind.
    const leftover = document.querySelector("#mermaid-diagram");
    if (leftover) {
      leftover.remove();
    }
  }
}