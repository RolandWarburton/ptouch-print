import { assertEquals, assertThrows } from "@std/assert";
import { authMac, encodeRequest, Op, ResponseAssembler } from "./protocol.ts";

const enc = new TextEncoder();

Deno.test("an empty request is one 6-byte chunk", () => {
  assertEquals(encodeRequest(7, Op.Status, new Uint8Array(), 20), [new Uint8Array([7, 2, 0, 0, 0, 0])]);
});

Deno.test("requests are split at the chunk size and join back up", () => {
  const payload = Uint8Array.from({ length: 100 }, (_, i) => i);
  const chunks = encodeRequest(3, Op.Image, payload, 20);
  for (const c of chunks) {
    if (c.length > 20) throw new Error(`chunk of ${c.length} bytes`);
  }
  assertEquals([...chunks[0].subarray(0, 6)], [3, 4, 100, 0, 0, 0]);
  for (const c of chunks.slice(1)) assertEquals([c[0], c[1]], [3, 0]);
  const joined = [...chunks[0].subarray(6), ...chunks.slice(1).flatMap((c) => [...c.subarray(2)])];
  assertEquals(joined, [...payload]);
  // 14 bytes in the first chunk, then 18 per chunk: 14 + 5*18 = 104 >= 100.
  assertEquals(chunks.length, 6);
});

Deno.test("chunks below the ATT minimum are refused", () => {
  assertThrows(() => encodeRequest(1, Op.Status, new Uint8Array(), 19), RangeError);
});

Deno.test("responses are joined until the more flag is clear", () => {
  const a = new ResponseAssembler();
  const body = enc.encode(JSON.stringify({ ok: true, tape_mm: 24 }));
  assertEquals(a.push(new Uint8Array([5, 1, ...body.subarray(0, 10)])), null);
  assertEquals(a.push(new Uint8Array([5, 0, ...body.subarray(10)])), { id: 5, body: { ok: true, tape_mm: 24 } });
});

// The same vector is checked against the Go server in internal/api.
Deno.test("auth MAC matches the server", async () => {
  const nonce = [...Array(16).keys()].map((b) => b.toString(16).padStart(2, "0")).join("");
  const mac = await authMac("0123456789abcdef\n", nonce);
  assertEquals(
    [...mac].map((b) => b.toString(16).padStart(2, "0")).join(""),
    "3495567ceba6ac03818bcf1b28e904fbe5899e21ae2245b03d92bcaa14b71d27",
  );
});
