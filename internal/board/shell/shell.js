// shell.js -- the token-holding page. It reads the token from the fragment,
// asks the loopback server for the board's bytes, and renders them in a blob
// iframe that runs in an opaque origin.
//
// The split is the whole point: this document is same-origin with the API, so
// it can hold the token and read the board. The iframe is sandboxed without
// allow-same-origin, so the board's own scripts run somewhere the API is
// unreachable from and the token is unreadable. The board therefore sees a
// document and nothing else.
//
// The board's bytes are rendered with overlay.js spliced into them in memory,
// here, just before the blob is built. Nothing is injected on disk and nothing
// is injected on the server: GET /api/board returns the file's bytes exactly,
// and this is the only place the text is altered. The overlay needs to run in
// the frame because the frame is the only place a hover outline or a pin can be
// drawn -- the shell cannot reach into an opaque origin.
(function () {
  "use strict";

  var statusEl = document.getElementById("status");
  var bannerEl = document.getElementById("banner");
  var frame = document.createElement("iframe");
  frame.id = "frame";
  frame.setAttribute("sandbox", "allow-scripts");
  frame.setAttribute("title", "board");

  var boardEtag = "";
  var blobURL = "";
  var overlaySource = "";

  // tokenFromFragment reads the per-run token out of "#t=...". It is a fragment
  // rather than a query so the token never rides a request line or a log.
  function tokenFromFragment(hash) {
    var match = /(?:^|[#&])t=([^&]*)/.exec(hash || "");
    return match ? decodeURIComponent(match[1]) : "";
  }

  function showBanner(message) {
    bannerEl.textContent = message;
    bannerEl.className = "on";
  }

  function setStatus(message) {
    statusEl.textContent = message;
  }

  // fetchOverlaySource reads overlay.js once, so a re-render does not re-read
  // it. A board with no overlay still renders: the board is the deliverable and
  // comment mode is an addition to it.
  function fetchOverlaySource(token) {
    return fetch("overlay.js").then(function (res) {
      if (!res.ok) return "";
      return res.text();
    }).catch(function () {
      return "";
    });
  }

  // withOverlay splices the overlay into the board's HTML, before the last
  // closing body tag when there is one and at the end when there is not. The
  // board's own markup is otherwise untouched: the overlay is appended, never
  // substituted into anything.
  function withOverlay(html) {
    if (!overlaySource) return html;
    var tag = "<scr" + "ipt>" + overlaySource + "</scr" + "ipt>";
    var lower = html.toLowerCase();
    var at = lower.lastIndexOf("</body>");
    if (at < 0) return html + tag;
    return html.slice(0, at) + tag + html.slice(at);
  }

  // render builds the board document and points the iframe at it. A blob URL
  // keeps the board's bytes out of the network and out of this document's DOM,
  // and the sandbox attribute above is what makes the origin opaque. The
  // previous blob URL is revoked: a re-render every two seconds would otherwise
  // leak one document per poll.
  function render(html) {
    var blob = new Blob([withOverlay(html)], { type: "text/html;charset=utf-8" });
    var next = URL.createObjectURL(blob);
    frame.src = next;
    if (blobURL) URL.revokeObjectURL(blobURL);
    blobURL = next;
    setStatus("");
    if (!frame.parentNode) document.body.appendChild(frame);
  }

  function fail(message) {
    setStatus(message);
  }

  // onDocument registers the comment layer's listener for the first board
  // document. There is one read of the board and both files share it.
  var documentListener = null;
  function onDocument(fn) {
    documentListener = fn;
    if (lastDoc) fn(lastDoc);
  }

  var lastDoc = null;

  function load() {
    var token = tokenFromFragment(window.location.hash);
    if (!token) {
      fail("no board token in the URL fragment; reopen the printed board URL");
      return;
    }
    fetchOverlaySource(token).then(function (src) {
      overlaySource = src;
      return fetch("api/board", { headers: { "X-Relevo-Board-Token": token } });
    }).then(function (res) {
      if (!res.ok) {
        throw new Error("the board server answered " + res.status);
      }
      return res.json();
    }).then(function (doc) {
      lastDoc = doc;
      boardEtag = doc.etag || "";
      if (doc.external && doc.external.length) {
        showBanner(
          "this board references " + doc.external.length +
            " external resource(s), which the board's content security policy blocks: " +
            doc.external.join(", ")
        );
      }
      if (doc.isNew || !doc.html) {
        setStatus("no board yet at " + (doc.name || "this path") + "-- save one and reload");
        return;
      }
      render(doc.html);
      if (documentListener) documentListener(doc);
    }).catch(function (err) {
      fail("could not load the board: " + err.message);
    });
  }

  // window.__relevo is the small surface comments.js shares, so there is one
  // token read and one board read in the document rather than one per file.
  window.__relevo = {
    tokenFromFragment: tokenFromFragment,
    render: render,
    showBanner: showBanner,
    setStatus: setStatus,
    onDocument: onDocument
  };

  load();
})();
