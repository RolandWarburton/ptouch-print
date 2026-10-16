// The page: connect over Web Bluetooth, show printer status, and print
// barcodes, text labels or PNGs.

import { PrinterClient, type Progress } from "./client.ts";
import type { ErrorResponse, PrintResponse, StatusResponse } from "./protocol.ts";

const KEY_STORAGE = "ptouch-api-key";

const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T;

const ui = {
  unsupported: $("unsupported"),
  apiKey: $<HTMLInputElement>("api-key"),
  remember: $<HTMLInputElement>("remember"),
  connect: $<HTMLButtonElement>("connect"),
  connState: $("conn-state"),
  printer: $("printer"),
  refresh: $<HTMLButtonElement>("refresh"),
  status: $("status"),
  barcodeForm: $<HTMLFormElement>("barcode-form"),
  barcodeText: $<HTMLInputElement>("barcode-text"),
  textForm: $<HTMLFormElement>("text-form"),
  labelText: $<HTMLInputElement>("label-text"),
  textPreview: $<HTMLCanvasElement>("text-preview"),
  imageForm: $<HTMLFormElement>("image-form"),
  imageFile: $<HTMLInputElement>("image-file"),
  imageInfo: $("image-info"),
  progress: $<HTMLProgressElement>("progress"),
  log: $("log"),
};

let client: PrinterClient | null = null;
let status: StatusResponse | null = null;
let busy = false;

function log(msg: string, kind: "info" | "ok" | "error" = "info") {
  const line = document.createElement("div");
  line.className = kind;
  line.textContent = `${new Date().toLocaleTimeString()}  ${msg}`;
  ui.log.prepend(line);
}

// localStorage can be missing or throw (private windows, blocked site data).
function storage(): Storage | null {
  try {
    const s = globalThis.localStorage;
    s.length;
    return s;
  } catch {
    return null;
  }
}

function render() {
  const connected = client?.connected ?? false;
  ui.connect.textContent = connected ? "Disconnect" : "Connect";
  ui.connect.disabled = busy && !connected;
  ui.apiKey.disabled = connected;
  ui.connState.textContent = connected
    ? `Connected to ${client!.device.name ?? "printer"} (chunk ${client!.info.chunk} bytes)`
    : "Not connected";
  ui.printer.hidden = !connected;
  for (const el of ui.printer.querySelectorAll<HTMLButtonElement>("button")) el.disabled = busy;
  renderStatus();
}

function statusText(s: StatusResponse): string {
  const parts = [`${s.tape_mm} mm tape`];
  parts.push(s.supported ? `${s.height_dots} dots high` : "not supported");
  if (s.errors.length) parts.push(`problems: ${s.errors.join(", ")}`);
  return parts.join(" · ");
}

function renderStatus() {
  ui.status.textContent = status ? statusText(status) : "Unknown";
  ui.status.className = status && (status.errors.length || !status.supported) ? "warn" : "";
}

function describeError(r: ErrorResponse): string {
  let msg = `${r.error} (${r.code})`;
  if (r.printer_errors?.length) msg += `: ${r.printer_errors.join(", ")}`;
  if (r.may_have_printed) msg += ". The label may have printed: check before trying again.";
  return msg;
}

/** Runs one action with the buttons disabled, logging what it returns. */
async function act(what: string, fn: () => Promise<void>) {
  if (busy) return;
  busy = true;
  render();
  try {
    await fn();
  } catch (e) {
    log(`${what}: ${e instanceof Error ? e.message : e}`, "error");
  } finally {
    busy = false;
    ui.progress.hidden = true;
    render();
  }
}

async function connect() {
  const key = ui.apiKey.value.trim();
  if (key.length < 16) {
    log("Enter the API key first (at least 16 characters).", "error");
    return;
  }
  await act("Connecting", async () => {
    log("Choose the printer in the browser's list…");
    client = await PrinterClient.connect(key, () => {
      log("Disconnected", "error");
      client = null;
      status = null;
      render();
    });
    const s = storage();
    if (ui.remember.checked) s?.setItem(KEY_STORAGE, key);
    else s?.removeItem(KEY_STORAGE);
    log(`Connected and authenticated to ${client.device.name ?? "the printer"}`, "ok");
    await refreshStatus();
  });
}

async function refreshStatus() {
  if (!client) return;
  const r = await client.status();
  if (!r.ok) {
    status = null;
    log(`Status: ${describeError(r)}`, "error");
    return;
  }
  status = r;
  log(`Status: ${statusText(r)}`, "ok");
  if (r.height_dots) drawText(ui.labelText.value || " ", r.height_dots);
}

function reportPrint(r: PrintResponse | ErrorResponse) {
  if (r.ok) log(`Printed ${r.length_mm.toFixed(1)} mm on ${r.tape_mm} mm tape`, "ok");
  else log(`Print failed: ${describeError(r)}`, "error");
}

function showProgress(): Progress {
  ui.progress.hidden = false;
  ui.progress.value = 0;
  return (sent, total) => {
    ui.progress.max = total;
    ui.progress.value = sent;
  };
}

// Text labels are drawn here at the loaded tape's height and sent as a PNG.
function drawText(text: string, height: number): HTMLCanvasElement {
  const c = ui.textPreview;
  const ctx = c.getContext("2d")!;
  const font = `bold ${Math.round(height * 0.75)}px system-ui, sans-serif`;
  ctx.font = font;
  const pad = Math.round(height * 0.25);
  c.width = Math.max(1, Math.ceil(ctx.measureText(text).width) + 2 * pad);
  c.height = height;
  ctx.fillStyle = "#fff";
  ctx.fillRect(0, 0, c.width, c.height);
  ctx.fillStyle = "#000";
  ctx.font = font; // resizing the canvas resets the context
  ctx.textBaseline = "middle";
  ctx.fillText(text, pad, height / 2);
  // Threshold to pure black and white so the printer gets crisp edges.
  const img = ctx.getImageData(0, 0, c.width, c.height);
  for (let i = 0; i < img.data.length; i += 4) {
    const v = img.data[i] < 128 ? 0 : 255;
    img.data[i] = img.data[i + 1] = img.data[i + 2] = v;
    img.data[i + 3] = 255;
  }
  ctx.putImageData(img, 0, 0);
  return c;
}

function canvasPNG(c: HTMLCanvasElement): Promise<Uint8Array> {
  return new Promise((resolve, reject) =>
    c.toBlob(async (b) => {
      if (!b) return reject(new Error("couldn't encode the label"));
      resolve(new Uint8Array(await b.arrayBuffer()));
    }, "image/png")
  );
}

function needStatus(): StatusResponse {
  if (!status?.supported || !status.height_dots) {
    throw new Error("refresh the status first; the loaded tape must be 12 or 24 mm");
  }
  return status;
}

ui.connect.addEventListener("click", () => {
  if (client?.connected) client.disconnect();
  else connect();
});

ui.refresh.addEventListener("click", () => act("Status", refreshStatus));

ui.barcodeForm.addEventListener("submit", (e) => {
  e.preventDefault();
  act("Barcode", async () => {
    const text = ui.barcodeText.value.trim();
    log(`Printing barcode ${text}…`);
    reportPrint(await client!.barcode(text));
  });
});

ui.labelText.addEventListener("input", () => {
  if (status?.height_dots) drawText(ui.labelText.value || " ", status.height_dots);
});

ui.textForm.addEventListener("submit", (e) => {
  e.preventDefault();
  act("Text label", async () => {
    const st = needStatus();
    const png = await canvasPNG(drawText(ui.labelText.value, st.height_dots!));
    log(`Printing "${ui.labelText.value}" (${png.length} bytes)…`);
    reportPrint(await client!.image(st.tape_mm, png, showProgress()));
  });
});

ui.imageFile.addEventListener("change", async () => {
  const f = ui.imageFile.files?.[0];
  if (!f) return;
  const bmp = await createImageBitmap(f);
  const want = status?.height_dots;
  ui.imageInfo.textContent = `${bmp.width} × ${bmp.height} px, ${f.size} bytes` +
    (want && bmp.height !== want ? ` (needs to be ${want} px high)` : "");
  ui.imageInfo.className = want && bmp.height !== want ? "warn" : "";
});

ui.imageForm.addEventListener("submit", (e) => {
  e.preventDefault();
  act("Image", async () => {
    const st = needStatus();
    const f = ui.imageFile.files?.[0];
    if (!f) throw new Error("choose a PNG first");
    if (f.type !== "image/png") throw new Error("the file must be a PNG");
    const png = new Uint8Array(await f.arrayBuffer());
    log(`Sending ${f.name} (${png.length} bytes)…`);
    reportPrint(await client!.image(st.tape_mm, png, showProgress()));
  });
});

// Start-up.
if (!navigator.bluetooth) {
  ui.unsupported.hidden = false;
  ui.connect.disabled = true;
} else {
  ui.apiKey.value = storage()?.getItem(KEY_STORAGE) ?? "";
  ui.remember.checked = ui.apiKey.value !== "";
}
render();
