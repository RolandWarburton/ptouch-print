// Serves dist/ on localhost. Web Bluetooth needs a secure context, and
// http://localhost counts as one.
//   deno task serve [port]

const port = Number(Deno.args[0] ?? 8000);
const root = new URL("./dist/", import.meta.url);
const types: Record<string, string> = {
  html: "text/html; charset=utf-8",
  js: "text/javascript; charset=utf-8",
  map: "application/json",
};

Deno.serve({ hostname: "127.0.0.1", port }, async (req) => {
  let path = new URL(req.url).pathname.replace(/^\/+/, "");
  if (path === "") path = "index.html";
  if (path.split("/").includes("..")) return new Response("bad path", { status: 400 });
  try {
    const body = await Deno.readFile(new URL(path, root));
    const ext = path.split(".").pop() ?? "";
    return new Response(body, { headers: { "content-type": types[ext] ?? "application/octet-stream" } });
  } catch {
    return new Response("not found", { status: 404 });
  }
});
