// Seals a fixed value with the spike page's own script under Node.js's
// WebCrypto and prints the fixture intake's tests read (seal-v1.json).
// Run from the repository root:
//   node intake/testdata/seal.mjs > intake/testdata/seal-v1.json
// The recipient's private key is a fixed test scalar; nothing here is real.
import { readFileSync } from "node:fs";
import { webcrypto } from "node:crypto";

const page = readFileSync("docs/guide/intake/spike/index.html", "utf8");
const script = page.match(/<script>([\s\S]*?)<\/script>/)[1];
// Everything up to the page's own functions that touch the DOM.
const lib = script.slice(script.indexOf("const te ="), script.indexOf("function oauthMode"))
  .replace(/^const app = .*$/m, "")
  .replace(/^function el\([\s\S]*?^}\n/m, "")
  .replace(/^async function copyLine[\s\S]*?^}\n/m, "")
  .replace(/^function showLine[\s\S]*?^}\n/m, "");
const self = { crypto: webcrypto };
const mod = new Function("self", "btoa", "atob", lib + "\nreturn { te, b64u, cat, hpkeSeal };");
const { te, b64u, cat, hpkeSeal } = mod(self, globalThis.btoa, globalThis.atob);

// A fixed P-256 scalar; its public key is derived in Go by the test as well.
const d = "c9afa9d845ba75166b5c215767b1d6934e50c3db36e89b127b8a622b120f6721";
const jwkPriv = await webcrypto.subtle.importKey("pkcs8", pkcs8(d), { name: "ECDH", namedCurve: "P-256" }, true, ["deriveBits"]);
const jwk = await webcrypto.subtle.exportKey("jwk", jwkPriv);
const pub = await webcrypto.subtle.importKey("jwk", { kty: "EC", crv: "P-256", x: jwk.x, y: jwk.y }, { name: "ECDH", namedCurve: "P-256" }, true, []);
const pk = new Uint8Array(await webcrypto.subtle.exportKey("raw", pub));

const id = "AAECAwQFBgcICQoLDA0ODw";
const name = "shop_api_key";
const value = "s3cr3t-value:with/odd+chars";
const info = cat(te.encode("ganglion-intake-v1"), new Uint8Array([0]), te.encode(id), new Uint8Array([0]), te.encode(name));
const sealed = await hpkeSeal(pk, info, te.encode(value));
console.log(JSON.stringify({ private_key: d, public_key: b64u(pk), id, name, value, line: "gsec1." + id + "." + b64u(sealed) }, null, 2));

// pkcs8 wraps a raw P-256 scalar in the minimal PKCS#8 DER WebCrypto accepts.
function pkcs8(hex) {
  const head = "308141020100301306072a8648ce3d020106082a8648ce3d030107042730250201010420";
  return Uint8Array.from((head + hex).match(/../g).map((b) => parseInt(b, 16)));
}
