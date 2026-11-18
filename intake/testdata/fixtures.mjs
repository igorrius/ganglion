// Runs the intake page's own script under Node.js's WebCrypto and writes the
// fixtures intake's tests read: a sealed gsec1 line (seal-v1.json) and a
// gauth1 line (oauth-v1.json). Run from the repository root:
//   node intake/testdata/fixtures.mjs [out-dir]
// The out-dir is intake/testdata by default; CI writes to a temporary one
// and points the Go tests at it with INTAKE_FIXTURES (an absolute path), so
// every run seals afresh. The recipient's private key is a fixed test scalar; nothing here
// is real.
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { webcrypto } from "node:crypto";

const out = process.argv[2] || "intake/testdata";
const page = readFileSync("intake/page/v1/index.html", "utf8");
const begin = page.indexOf("// --- pure: begin"), end = page.indexOf("// --- pure: end");
if (begin < 0 || end < begin) throw new Error("the page has no pure part");
const mod = new Function("self", "btoa", "atob", page.slice(begin, end) + "\nreturn { oauthLine, secretRequest, secretLine };");
const { oauthLine, secretRequest, secretLine } = mod({ crypto: webcrypto }, globalThis.btoa, globalThis.atob);

// A fixed P-256 scalar; the Go test derives its public key as well.
const d = "c9afa9d845ba75166b5c215767b1d6934e50c3db36e89b127b8a622b120f6721";
const priv = await webcrypto.subtle.importKey("pkcs8", pkcs8(d), { name: "ECDH", namedCurve: "P-256" }, true, ["deriveBits"]);
const jwk = await webcrypto.subtle.exportKey("jwk", priv);
const pub = await webcrypto.subtle.importKey("jwk", { kty: "EC", crv: "P-256", x: jwk.x, y: jwk.y }, { name: "ECDH", namedCurve: "P-256" }, true, []);
const pk = new Uint8Array(await webcrypto.subtle.exportKey("raw", pub));
const pkB64u = Buffer.from(pk).toString("base64url");

// The secret goes through the page's own parsing of the node's link.
const id = "AAECAwQFBgcICQoLDA0ODw";
const name = "shop_api_key";
const value = "s3cr3t-välue:with/odd+chars \u{1F511}";
const fragment = new URLSearchParams({ v: "1", mode: "secret", id, name, host: "api.example.com", pk: pkB64u });
const req = secretRequest(fragment);
if (!req) throw new Error("the page refused the request");
const line = await secretLine(req, value);
write("seal-v1.json", { private_key: d, public_key: pkB64u, id, name, value, line });

// The OAuth code holds characters a code may hold (':' broke the spike page).
const state = "AQIDBAUGBwgJCgsMDQ4PEA";
const code = "c0de:with/odd+chars=~.%";
const iss = "https://auth.example.com";
write("oauth-v1.json", {
  state, code, iss,
  line: oauthLine(state, code, iss),
  line_no_iss: oauthLine(state, code, ""),
});

function write(file, v) { writeFileSync(join(out, file), JSON.stringify(v, null, 2) + "\n"); }

// pkcs8 wraps a raw P-256 scalar in the minimal PKCS#8 DER WebCrypto accepts.
function pkcs8(hex) {
  const head = "308141020100301306072a8648ce3d020106082a8648ce3d030107042730250201010420";
  return Uint8Array.from((head + hex).match(/../g).map((b) => parseInt(b, 16)));
}
