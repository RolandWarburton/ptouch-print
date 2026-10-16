// Framing and authentication for ptouch-print-server's Bluetooth API. Nothing
// here touches Bluetooth, so it can be tested without a browser.

export const SERVICE_UUID = "c30d0001-ccea-4d82-aa2f-1ed9328fb057";
export const INFO_UUID = "c30d0002-ccea-4d82-aa2f-1ed9328fb057";
export const REQUEST_UUID = "c30d0003-ccea-4d82-aa2f-1ed9328fb057";
export const RESPONSE_UUID = "c30d0004-ccea-4d82-aa2f-1ed9328fb057";

export const PROTOCOL_VERSION = 1;

export const Op = {
  Auth: 0x01,
  Status: 0x02,
  Barcode: 0x03,
  Image: 0x04,
} as const;
export type Op = (typeof Op)[keyof typeof Op];

const START_HEADER = 6; // req_id, op, u32 LE length
const CONT_HEADER = 2; // req_id, 0x00
const FLAG_MORE = 0x01;

/** The value of the info characteristic. */
export interface Info {
  proto: number;
  nonce: string;
  chunk: number;
  max_msg: number;
  authenticated: boolean;
}

export interface ErrorResponse {
  ok: false;
  error: string;
  code: string;
  printer_errors?: string[];
  loaded_tape_mm?: number;
  may_have_printed?: boolean;
}

export interface StatusResponse {
  ok: true;
  tape_mm: number;
  supported: boolean;
  height_dots?: number;
  geometry_verified?: boolean;
  media_type: number;
  dpi: number;
  errors: string[];
}

export interface PrintResponse {
  ok: true;
  printed: boolean;
  tape_mm: number;
  length_dots: number;
  length_mm: number;
}

export interface AuthResponse {
  ok: true;
  authenticated: boolean;
}

/** Splits a request into writes of at most chunk bytes. */
export function encodeRequest(id: number, op: Op, payload: Uint8Array, chunk: number): Uint8Array<ArrayBuffer>[] {
  if (chunk < 20) throw new RangeError(`chunk ${chunk} is below the minimum of 20`);
  const first = new Uint8Array(Math.min(START_HEADER + payload.length, chunk));
  first[0] = id;
  first[1] = op;
  new DataView(first.buffer).setUint32(2, payload.length, true);
  let n = first.length - START_HEADER;
  first.set(payload.subarray(0, n), START_HEADER);
  const chunks = [first];
  while (n < payload.length) {
    const part = payload.subarray(n, n + chunk - CONT_HEADER);
    const c = new Uint8Array(CONT_HEADER + part.length);
    c[0] = id;
    c.set(part, CONT_HEADER);
    chunks.push(c);
    n += part.length;
  }
  return chunks;
}

/** Joins response indications back into complete JSON bodies. */
export class ResponseAssembler {
  #parts = new Map<number, Uint8Array[]>();

  /** Takes one indication. Returns the response once its last chunk arrives. */
  push(frame: Uint8Array): { id: number; body: unknown } | null {
    if (frame.length < 2) throw new Error("response chunk is shorter than its header");
    const id = frame[0];
    const parts = this.#parts.get(id) ?? [];
    parts.push(frame.slice(2));
    if (frame[1] & FLAG_MORE) {
      this.#parts.set(id, parts);
      return null;
    }
    this.#parts.delete(id);
    const all = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
    let off = 0;
    for (const p of parts) {
      all.set(p, off);
      off += p.length;
    }
    return { id, body: JSON.parse(new TextDecoder().decode(all)) };
  }

  reset() {
    this.#parts.clear();
  }
}

export function hexToBytes(hex: string): Uint8Array {
  if (hex.length % 2 !== 0 || !/^[0-9a-f]*$/i.test(hex)) throw new Error("bad hex");
  const b = new Uint8Array(hex.length / 2);
  for (let i = 0; i < b.length; i++) b[i] = parseInt(hex.slice(2 * i, 2 * i + 2), 16);
  return b;
}

/** HMAC-SHA256(API key, "ptouch-auth-v1" || nonce), the auth payload. */
export async function authMac(apiKey: string, nonceHex: string): Promise<Uint8Array> {
  const enc = new TextEncoder();
  const key = await crypto.subtle.importKey(
    "raw",
    enc.encode(apiKey.trim()),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const context = enc.encode("ptouch-auth-v1");
  const nonce = hexToBytes(nonceHex);
  const msg = new Uint8Array(context.length + nonce.length);
  msg.set(context);
  msg.set(nonce, context.length);
  return new Uint8Array(await crypto.subtle.sign("HMAC", key, msg));
}
