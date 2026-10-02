// The relevo board page: one Excalidraw scene served by `relevo board`, saved
// back over the small JSON API, plus a Mermaid panel that renders a diagram to
// text and imports it onto the canvas. The token rides in the URL fragment and
// is read here, never in a request line.

import { createRoot } from "react-dom/client";
import { useCallback, useEffect, useRef, useState } from "react";
import { Excalidraw, exportToSvg, serializeAsJSON } from "@excalidraw/excalidraw";
import { convert, roleOf } from "./mermaid.js";
import { importElements } from "./import.js";

const TOKEN = new URLSearchParams(window.location.hash.slice(1)).get("t") || "";

// api wraps fetch with the board token on every call. The Host is checked by
// the server, so a foreign page cannot reach this API.
function api(path, options) {
  const opts = Object.assign({}, options);
  opts.headers = Object.assign({ "X-Relevo-Board-Token": TOKEN }, opts.headers || {});
  return fetch(path, opts);
}

// firstLine is the one line of a thrown error the notices and the preview
// carry: a Mermaid parse failure prints several, and only the first names the
// line.
function firstLine(err) {
  const text = err && err.message ? err.message : String(err);
  return text.split("\n")[0];
}

// themeAppState builds the appState a new scene starts with, from the palette
// the server sent: new elements draw in the theme's ink, and the view
// background is the palette's background so what is stored and shown match.
function themeAppState(theme) {
  return {
    theme: "light",
    viewBackgroundColor: theme.bg,
    currentItemStrokeColor: theme.ink,
    currentItemBackgroundColor: "transparent",
    currentItemFontFamily: 3,
    currentItemRoughness: 0,
  };
}

function App() {
  const [doc, setDoc] = useState(null);
  const [notice, setNotice] = useState("");
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);
  const [mermaidOpen, setMermaidOpen] = useState(false);
  const [mermaidText, setMermaidText] = useState("");
  const [preview, setPreview] = useState("");
  const apiRef = useRef(null);
  const etagRef = useRef("");

  useEffect(() => {
    let cancelled = false;
    // Fonts are awaited before initialData is handed over: text measured before
    // the font loaded renders clipped in Excalidraw.
    Promise.all([
      document.fonts.load('16px "Cascadia"').catch(() => {}),
      document.fonts.load('16px "Excalifont"').catch(() => {}),
      document.fonts.load('16px "Assistant"').catch(() => {}),
    ])
      .then(() => document.fonts.ready)
      .then(() => api("/api/scene"))
      .then((res) => {
        if (!res.ok) throw new Error("load failed: " + res.status);
        return res.json();
      })
      .then((body) => {
        if (cancelled) return;
        etagRef.current = body.etag || "";
        setDoc(body);
      })
      .catch((err) => {
        if (!cancelled) setNotice(String(err));
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const save = useCallback(async () => {
    const apiRefCurrent = apiRef.current;
    if (!apiRefCurrent || busy) return;
    setBusy(true);
    try {
      const elements = apiRefCurrent.getSceneElements();
      const appState = apiRefCurrent.getAppState();
      const files = apiRefCurrent.getFiles();
      const scene = JSON.parse(serializeAsJSON(elements, appState, files, "local"));
      const svg = await exportToSvg({
        elements,
        appState: Object.assign({}, appState, { exportWithDarkMode: false }),
        files,
      });
      const res = await api("/api/scene", {
        method: "PUT",
        headers: { "Content-Type": "application/json", "If-Match": etagRef.current },
        body: JSON.stringify({ scene, svg: svg.outerHTML }),
      });
      if (res.status === 409) {
        setNotice("the file changed on disk; reload to continue");
        setDirty(false);
        return;
      }
      if (!res.ok) {
        setNotice("save failed: " + res.status);
        return;
      }
      const body = await res.json();
      etagRef.current = body.etag || "";
      setNotice("saved " + new Date().toLocaleTimeString());
      setDirty(false);
    } catch (err) {
      setNotice(String(err));
    } finally {
      setBusy(false);
    }
  }, [busy]);

  useEffect(() => {
    // The hand check drives the save through the button; Ctrl-S is the same
    // action for a human. Excalidraw binds Ctrl-S on `document` in the capture
    // phase, so this listener must capture on `window` -- ahead of it -- and
    // stop the event there, or Excalidraw's own save runs and this one, which
    // sits in the bubble phase, is never reached.
    const onKey = (e) => {
      if ((e.ctrlKey || e.metaKey) && (e.key === "s" || e.key === "S")) {
        e.preventDefault();
        e.stopPropagation();
        save();
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [save]);

  // render shows the diagram's structure as text. It never touches the canvas:
  // an invalid definition writes only the preview.
  const render = useCallback(async () => {
    if (!doc) return;
    if (mermaidText.trim() === "") {
      setPreview("nothing to render");
      return;
    }
    try {
      const converted = await convert(mermaidText, doc.theme);
      setPreview(
        converted.skeletons
          .map((element) =>
            [
              roleOf(element, converted.skeletons),
              element.id,
              (element.label && element.label.text) || "",
            ].join("\t"),
          )
          .join("\n"),
      );
    } catch (err) {
      setPreview("mermaid: " + firstLine(err));
    }
  }, [doc, mermaidText]);

  // doImport appends the diagram to the canvas. On failure it leaves the scene,
  // the dirty flag and the textarea alone.
  const doImport = useCallback(async () => {
    const apiRefCurrent = apiRef.current;
    if (!doc || !apiRefCurrent) return;
    if (mermaidText.trim() === "") {
      setNotice("nothing to import");
      return;
    }
    try {
      const converted = await convert(mermaidText, doc.theme);
      const count = importElements(apiRefCurrent, converted);
      setNotice("imported " + count + " elements");
      setDirty(true);
    } catch (err) {
      setNotice("mermaid: " + firstLine(err));
    }
  }, [doc, mermaidText]);

  const initialData = doc
    ? doc.isNew
      ? { elements: [], appState: themeAppState(doc.theme), files: {} }
      : doc.scene
    : null;

  // The Mermaid surface takes its colours from the theme the server sent, so
  // the page carries no second palette; the stylesheet holds layout only.
  const surface = doc
    ? { background: doc.theme.bg, color: doc.theme.ink, borderColor: doc.theme.line }
    : undefined;

  return (
    <div className="board-root">
      <div className="board-bar">
        <span className="board-name">{doc ? "relevo board" : "loading…"}</span>
        <span className="board-notice">{notice}</span>
        {doc && (
          <button
            className="board-mermaid-toggle"
            data-testid="mermaid-toggle"
            onClick={() => setMermaidOpen((open) => !open)}
            style={surface}
          >
            Mermaid
          </button>
        )}
        <button className="board-save" data-testid="save" disabled={!dirty || busy} onClick={save}>
          {busy ? "saving…" : dirty ? "Save" : "Saved"}
        </button>
      </div>
      {doc && mermaidOpen && (
        <div className="board-mermaid-panel">
          <textarea
            className="board-mermaid-text"
            data-testid="mermaid-text"
            value={mermaidText}
            onChange={(e) => setMermaidText(e.target.value)}
            style={surface}
            spellCheck={false}
          />
          <div className="board-mermaid-actions">
            <button
              className="board-mermaid-button"
              data-testid="mermaid-render"
              onClick={render}
              style={surface}
            >
              Render
            </button>
            <button
              className="board-mermaid-button"
              data-testid="mermaid-import"
              onClick={doImport}
              style={surface}
            >
              Import
            </button>
          </div>
          <pre className="board-mermaid-preview" data-testid="mermaid-preview" style={surface}>
            {preview}
          </pre>
        </div>
      )}
      <div className="board-canvas">
        {initialData && (
          <Excalidraw
            initialData={initialData}
            theme="light"
            excalidrawAPI={(apiInstance) => {
              apiRef.current = apiInstance;
              apiInstance.onChange(() => setDirty(true));
            }}
          />
        )}
      </div>
    </div>
  );
}

createRoot(document.getElementById("root")).render(<App />);