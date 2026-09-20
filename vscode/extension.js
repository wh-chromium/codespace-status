// codespace-status VS Code extension: a thin wrapper around the packaged CLI.
// It uses only Node built-ins and the VS Code API; the webview shares the same
// HTML, CSS and JavaScript as the standalone web UI and never talks to the web
// server.
"use strict";

const vscode = require("vscode");
const cp = require("child_process");
const fs = require("fs");
const path = require("path");

let panel = null;
let watcher = null;
let binary = "";

/** Resolves the CLI binary, preferring the user setting over the packaged one. */
function resolveBinary(context) {
  const configured = vscode.workspace.getConfiguration("codespaceStatus").get("binaryPath");
  if (configured) return configured;
  const name = process.platform === "win32" ? "codespace-status.exe" : "codespace-status";
  const packaged = path.join(context.extensionPath, "bin", name);
  if (fs.existsSync(packaged)) {
    try {
      fs.chmodSync(packaged, 0o755);
    } catch (e) {
      // Read-only install locations are fine as long as the bit is already set.
    }
    return packaged;
  }
  return "codespace-status";
}

/** Runs the CLI once and resolves with stdout. */
function run(args) {
  return new Promise((resolve, reject) => {
    cp.execFile(binary, args, { maxBuffer: 8 * 1024 * 1024 }, (err, stdout, stderr) => {
      if (err) reject(new Error((stderr || err.message).trim()));
      else resolve(stdout);
    });
  });
}

/** Runs the CLI once and parses its JSON output. */
async function runJSON(args) {
  const out = await run(args.concat(["--json"]));
  return JSON.parse(out);
}

/** Starts `watch --json`, forwarding each status line to the webview. */
function startWatcher() {
  stopWatcher();
  const child = cp.spawn(binary, ["watch", "--json"], { stdio: ["ignore", "pipe", "pipe"] });
  let buffer = "";
  child.stdout.on("data", (chunk) => {
    buffer += chunk.toString();
    let index = buffer.indexOf("\n");
    while (index >= 0) {
      const line = buffer.slice(0, index).trim();
      buffer = buffer.slice(index + 1);
      index = buffer.indexOf("\n");
      if (!line) continue;
      try {
        post({ type: "status", status: JSON.parse(line) });
      } catch (e) {
        // Ignore partial or non-JSON output.
      }
    }
  });
  child.stderr.on("data", (chunk) => post({ type: "notice", text: chunk.toString() }));
  child.on("exit", () => {
    if (watcher === child) watcher = null;
  });
  watcher = child;
}

/** Stops the streaming watcher if one is running. */
function stopWatcher() {
  if (watcher) {
    watcher.kill();
    watcher = null;
  }
}

/** Sends a message to the webview when it exists. */
function post(message) {
  if (panel) panel.webview.postMessage(message);
}

/** Builds the webview HTML from the shared media assets. */
function html(webview, context) {
  const media = (file) => webview.asWebviewUri(vscode.Uri.joinPath(context.extensionUri, "media", file));
  const file = path.join(context.extensionPath, "media", "index.html");
  let page = fs.readFileSync(file, "utf8");
  const csp = '<meta http-equiv="Content-Security-Policy" content="default-src \'none\'; ' +
    "style-src " + webview.cspSource + "; script-src " + webview.cspSource + ';" />';
  page = page.replace("<title>", csp + "\n  <title>");
  page = page.replace('<link rel="stylesheet" href="static/style.css" />',
    '<link rel="stylesheet" href="' + media("style.css") + '" />\n  <link rel="stylesheet" href="' + media("vscode.css") + '" />');
  page = page.replace('<script src="static/app.js"></script>',
    '<script src="' + media("bridge.js") + '"></script>\n  <script src="' + media("app.js") + '"></script>');
  return page;
}

/** Handles one RPC request coming from the webview. */
async function handle(message) {
  const { id, action, value } = message;
  try {
    let result = {};
    switch (action) {
      case "select":
        await run(["select", value]);
        break;
      case "deselect":
        await run(["deselect"]);
        break;
      case "sync":
        await run(["sync"]);
        break;
      case "setPoll":
        await run(["poll", String(value)]);
        break;
      case "setTheme":
        await run(["theme", String(value)]);
        break;
      case "permissions":
        result = await runJSON(["permissions"]);
        break;
      case "auth":
        openAuthTerminal();
        result = { output: "authentication started in the terminal", command: "gh auth login --scopes codespace --web" };
        break;
      default:
        throw new Error("unknown action: " + action);
    }
    post({ type: "reply", id: id, result: result });
  } catch (e) {
    post({ type: "reply", id: id, result: { error: String(e.message || e) } });
  }
}

/** Runs the interactive login in a VS Code terminal. */
function openAuthTerminal() {
  const terminal = vscode.window.createTerminal("codespace-status auth");
  terminal.show();
  terminal.sendText(binary + " auth");
}

/** Creates or reveals the status panel. */
function openPanel(context) {
  if (panel) {
    panel.reveal(vscode.ViewColumn.Active);
    return;
  }
  panel = vscode.window.createWebviewPanel("codespaceStatus", "Codespace Status", vscode.ViewColumn.Active, {
    enableScripts: true,
    retainContextWhenHidden: true,
    localResourceRoots: [vscode.Uri.joinPath(context.extensionUri, "media")],
  });
  panel.webview.html = html(panel.webview, context);
  panel.webview.onDidReceiveMessage(handle);
  panel.onDidDispose(() => {
    panel = null;
    stopWatcher();
  });
  startWatcher();
}

/** Activates the extension. */
function activate(context) {
  binary = resolveBinary(context);

  const pollMS = vscode.workspace.getConfiguration("codespaceStatus").get("pollMS");
  if (pollMS) run(["poll", String(pollMS)]).catch(() => {});

  context.subscriptions.push(
    vscode.commands.registerCommand("codespaceStatus.open", () => openPanel(context)),
    vscode.commands.registerCommand("codespaceStatus.sync", async () => {
      try {
        await run(["sync"]);
        vscode.window.showInformationMessage("codespace-status: codespaces synced");
      } catch (e) {
        vscode.window.showErrorMessage(String(e.message || e));
      }
    }),
    vscode.commands.registerCommand("codespaceStatus.select", async () => {
      try {
        const list = await runJSON(["list"]);
        const pick = await vscode.window.showQuickPick(
          list.map((cs) => ({ label: cs.name, description: cs.state + (cs.selected ? " (active)" : "") })),
          { title: "Select active codespace" });
        if (pick) await run(["select", pick.label]);
      } catch (e) {
        vscode.window.showErrorMessage(String(e.message || e));
      }
    }),
    vscode.commands.registerCommand("codespaceStatus.permissions", async () => {
      try {
        const p = await runJSON(["permissions"]);
        vscode.window.showInformationMessage(
          "gh authenticated: " + p.authenticated + ", codespace access: " + p.can_list_codespaces);
      } catch (e) {
        vscode.window.showErrorMessage(String(e.message || e));
      }
    }),
    vscode.commands.registerCommand("codespaceStatus.auth", openAuthTerminal),
    { dispose: stopWatcher });
}

/** Deactivates the extension. */
function deactivate() {
  stopWatcher();
}

module.exports = { activate, deactivate };
