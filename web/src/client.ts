// A Web Bluetooth session with ptouch-print-server, in the order the server
// needs: read info (which pairs), subscribe to responses, authenticate, then one
// request at a time.

import {
  authMac,
  type AuthResponse,
  encodeRequest,
  type ErrorResponse,
  type Info,
  INFO_UUID,
  Op,
  type PrintResponse,
  PROTOCOL_VERSION,
  REQUEST_UUID,
  RESPONSE_UUID,
  ResponseAssembler,
  SERVICE_UUID,
  type StatusResponse,
} from "./protocol.ts";

/** Prints can wait 30 s for the printer, then take 20 s, over a slow link. */
const REQUEST_TIMEOUT_MS = 90_000;

export type Progress = (sent: number, total: number) => void;

export class PrinterClient {
  info!: Info;
  readonly device: BluetoothDevice;
  #server: BluetoothRemoteGATTServer;
  #infoChar!: BluetoothRemoteGATTCharacteristic;
  #requestChar!: BluetoothRemoteGATTCharacteristic;
  #responseChar!: BluetoothRemoteGATTCharacteristic;
  #assembler = new ResponseAssembler();
  #nextId = 0;
  #pending: { id: number; resolve: (v: unknown) => void; reject: (e: Error) => void } | null = null;
  #queue: Promise<unknown> = Promise.resolve();

  private constructor(device: BluetoothDevice, server: BluetoothRemoteGATTServer) {
    this.device = device;
    this.#server = server;
  }

  /** Shows the browser's device chooser, connects and authenticates. */
  static async connect(apiKey: string, onDisconnect: () => void): Promise<PrinterClient> {
    if (!navigator.bluetooth) throw new Error("this browser has no Web Bluetooth");
    const device = await navigator.bluetooth.requestDevice({ filters: [{ services: [SERVICE_UUID] }] });
    if (!device.gatt) throw new Error("the device has no GATT server");
    const server = await device.gatt.connect();
    const c = new PrinterClient(device, server);
    device.addEventListener("gattserverdisconnected", () => {
      c.#fail(new Error("disconnected"));
      onDisconnect();
    });
    try {
      await c.#start();
      await c.#auth(apiKey);
    } catch (e) {
      server.disconnect();
      throw e;
    }
    return c;
  }

  get connected(): boolean {
    return this.#server.connected;
  }

  disconnect() {
    this.#server.disconnect();
  }

  async #start() {
    const svc = await this.#server.getPrimaryService(SERVICE_UUID);
    this.#infoChar = await svc.getCharacteristic(INFO_UUID);
    this.#requestChar = await svc.getCharacteristic(REQUEST_UUID);
    this.#responseChar = await svc.getCharacteristic(RESPONSE_UUID);

    // Reading info first is what makes the browser pair, if needed.
    await this.refreshInfo();
    if (this.info.proto !== PROTOCOL_VERSION) {
      throw new Error(`the server speaks protocol ${this.info.proto}, this page speaks ${PROTOCOL_VERSION}`);
    }
    this.#responseChar.addEventListener("characteristicvaluechanged", () => this.#onIndication());
    await this.#responseChar.startNotifications();
  }

  async refreshInfo(): Promise<Info> {
    const v = await this.#infoChar.readValue();
    this.info = JSON.parse(new TextDecoder().decode(v));
    return this.info;
  }

  async #auth(apiKey: string) {
    const resp = await this.#request<AuthResponse>(Op.Auth, await authMac(apiKey, this.info.nonce));
    if (!resp.ok) throw new Error(`${resp.error} (${resp.code})`);
  }

  status(): Promise<StatusResponse | ErrorResponse> {
    return this.#request(Op.Status, new Uint8Array());
  }

  /** Prints a Code 128 barcode. tapeMM is optional; the server checks it against the loaded tape. */
  barcode(text: string, tapeMM?: number): Promise<PrintResponse | ErrorResponse> {
    const req = tapeMM ? { text, tape_mm: tapeMM } : { text };
    return this.#request(Op.Barcode, new TextEncoder().encode(JSON.stringify(req)));
  }

  /** Prints a PNG rendered for tapeMM tape. Its height must be the status's height_dots. */
  image(tapeMM: number, png: Uint8Array, progress?: Progress): Promise<PrintResponse | ErrorResponse> {
    const payload = new Uint8Array(1 + png.length);
    payload[0] = tapeMM;
    payload.set(png, 1);
    return this.#request(Op.Image, payload, progress);
  }

  /** Requests run one at a time; the server answers a second one with busy. */
  #request<T>(op: Op, payload: Uint8Array, progress?: Progress): Promise<T | ErrorResponse> {
    const run = this.#queue.then(() => this.#send<T>(op, payload, progress));
    this.#queue = run.catch(() => {});
    return run;
  }

  async #send<T>(op: Op, payload: Uint8Array, progress?: Progress): Promise<T | ErrorResponse> {
    if (payload.length > this.info.max_msg) {
      throw new Error(`message is ${payload.length} bytes, the server takes at most ${this.info.max_msg}`);
    }
    this.#nextId = (this.#nextId + 1) % 256;
    const id = this.#nextId;
    const answer = new Promise<unknown>((resolve, reject) => {
      this.#pending = { id, resolve, reject };
    });
    answer.catch(() => {}); // awaited below; this stops an early rejection being reported as unhandled
    const timer = setTimeout(() => this.#fail(new Error("no response from the server")), REQUEST_TIMEOUT_MS);
    try {
      const chunks = encodeRequest(id, op, payload, this.info.chunk);
      let sent = 0;
      for (const c of chunks) {
        await this.#requestChar.writeValueWithResponse(c);
        sent += c.length;
        progress?.(sent, chunks.reduce((n, x) => n + x.length, 0));
      }
      return (await answer) as T | ErrorResponse;
    } catch (e) {
      this.#pending = null;
      // The MTU can change; pick up the new chunk size for next time.
      if (this.connected) await this.refreshInfo().catch(() => {});
      throw e;
    } finally {
      clearTimeout(timer);
    }
  }

  #onIndication() {
    const v = this.#responseChar.value;
    if (!v) return;
    let resp;
    try {
      resp = this.#assembler.push(new Uint8Array(v.buffer, v.byteOffset, v.byteLength));
    } catch (e) {
      this.#fail(e instanceof Error ? e : new Error(String(e)));
      return;
    }
    if (resp && this.#pending?.id === resp.id) {
      const p = this.#pending;
      this.#pending = null;
      p.resolve(resp.body);
    }
  }

  #fail(err: Error) {
    this.#assembler.reset();
    const p = this.#pending;
    this.#pending = null;
    p?.reject(err);
  }
}
