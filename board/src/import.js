// The page's import path: place a converted Mermaid diagram beside what the
// canvas already holds and append it. Existing elements are never replaced.

// importGap is the distance a first import leaves below the scene's current
// bounds: Excalidraw's DEFAULT_GRID_SIZE, the same gap S2's annotate uses.
const importGap = 20;

// boundsOf is the bounding box of a scene's non-deleted elements: the leftmost
// x and the lowest bottom edge (y + height), plus the topmost y the diagram
// offset needs. An empty list reports empty; a missing numeric field counts as
// zero, as the S2 annotate rule reads it.
export function boundsOf(elements) {
  let minX = Infinity;
  let minY = Infinity;
  let maxBottom = -Infinity;
  let empty = true;
  for (const element of elements) {
    if (element.isDeleted) {
      continue;
    }
    empty = false;
    const x = typeof element.x === "number" ? element.x : 0;
    const y = typeof element.y === "number" ? element.y : 0;
    const height = typeof element.height === "number" ? element.height : 0;
    if (x < minX) minX = x;
    if (y < minY) minY = y;
    if (y + height > maxBottom) maxBottom = y + height;
  }
  return { empty, minX, minY, maxBottom };
}

// offsetFor is the S2 annotate rule: align the diagram's left edge with the
// scene's leftmost element and drop its top one gap below the scene's lowest
// edge. An empty scene places the diagram at the origin.
export function offsetFor(existingBounds, diagramBounds) {
  if (existingBounds.empty) {
    return { dx: 0, dy: 0 };
  }
  return {
    dx: existingBounds.minX - diagramBounds.minX,
    dy: existingBounds.maxBottom + importGap - diagramBounds.minY,
  };
}

// importElements appends the converted diagram to the canvas. It is the only
// api-touching part: it reads the existing elements, offsets the diagram with
// the annotate rule, appends (never replaces) with captureUpdate "NEVER" so
// Excalidraw's undo history is left as the user had it, and adds the diagram's
// files when it carries any. It returns the number of diagram shapes added,
// the skeleton count, which is what the notice reports.
export function importElements(api, converted) {
  const existing = api.getSceneElements();
  const offset = offsetFor(boundsOf(existing), boundsOf(converted.elements));
  const moved = converted.elements.map((element) =>
    Object.assign({}, element, { x: element.x + offset.dx, y: element.y + offset.dy }),
  );
  api.updateScene({ elements: [...existing, ...moved], captureUpdate: "NEVER" });
  const files = Object.values(converted.files || {});
  if (files.length > 0) {
    api.addFiles(files);
  }
  return converted.skeletons.length;
}