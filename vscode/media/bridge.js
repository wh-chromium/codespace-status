// Bridges the shared web UI to the VS Code extension host. The panel talks to
// the codespace-status CLI through postMessage and never contacts the web
// server, so the extension works with the web UI stopped.
"use strict";

(function () {
  const api = acquireVsCodeApi();
  const pending = new Map();
  const waiting = [];
  let latest = null;
  let nextId = 1;

  window.addEventListener("message", (event) => {
    const message = event.data || {};
    if (message.type === "status") {
      latest = message.status;
      while (waiting.length) waiting.shift()(latest);
    } else if (message.type === "reply") {
      const resolve = pending.get(message.id);
      if (resolve) {
        pending.delete(message.id);
        resolve(message.result || {});
      }
    }
  });

  function call(action, value) {
    return new Promise((resolve) => {
      const id = nextId++;
      pending.set(id, resolve);
      api.postMessage({ id: id, action: action, value: value });
    });
  }

  window.CS_VSCODE = true;
  window.CS_TRANSPORT = {
    status: () => (latest ? Promise.resolve(latest) : new Promise((resolve) => waiting.push(resolve))),
    select: (name) => call("select", name),
    deselect: () => call("deselect"),
    sync: () => call("sync"),
    setPoll: (ms) => call("setPoll", ms),
    setTheme: (value) => call("setTheme", value),
    permissions: () => call("permissions"),
    auth: () => call("auth"),
  };
})();
