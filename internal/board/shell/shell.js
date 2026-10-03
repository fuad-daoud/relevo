// shell.js -- the token-holding page. It reads the token from the fragment,
// asks the loopback server for the board's bytes, and renders them in a blob
// iframe that runs in an opaque origin.
//
// The split is the whole point: this document is same-origin with the API, so
// it can hold the token and read the board. The iframe is sandboxed without
// allow-same-origin, so the board's own scripts run somewhere the API is
// unreachable from and the token is unreadable. The board therefore sees a
// document and nothing else.
(function () {
  "use strict";

  var statusEl = document.getElementById("status");
  var bannerEl = document.getElementById("banner");
  var frame = document.createElement("iframe");
  frame.id = "frame";
  frame.setAttribute("sandbox", "allow-scripts");
  frame.setAttribute("title", "board");

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

  // render builds the board document and points the iframe at it. A blob URL
  // keeps the board's bytes out of the network and out of this document's DOM,
  // and the sandbox attribute above is what makes the origin opaque.
  function render(html) {
    var blob = new Blob([html], { type: "text/html;charset=utf-8" });
    frame.src = URL.createObjectURL(blob);
    setStatus("");
    document.body.appendChild(frame);
  }

  function fail(message) {
    setStatus(message);
  }

  function load() {
    var token = tokenFromFragment(window.location.hash);
    if (!token) {
      fail("no board token in the URL fragment; reopen the printed board URL");
      return;
    }
    fetch("api/board", { headers: { "X-Relevo-Board-Token": token } })
      .then(function (res) {
        if (!res.ok) {
          throw new Error("the board server answered " + res.status);
        }
        return res.json();
      })
      .then(function (doc) {
        if (doc.external && doc.external.length) {
          showBanner(
            "this board references " + doc.external.length +
              " external resource(s), which the board's content security policy blocks: " +
              doc.external.join(", ")
          );
        }
        if (doc.isNew || !doc.html) {
          setStatus("no board yet at " + (doc.name || "this path") + " -- save one and reload");
          return;
        }
        render(doc.html);
      })
      .catch(function (err) {
        fail("could not load the board: " + err.message);
      });
  }

  load();
})();