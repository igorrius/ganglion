# Remote MCP sign-in and the intake page, tried on a real server

- **Date:** 2026-11-18
- **For:** [ADR-0097](../adr/0097-connect-remote-mcp-servers-from-the-chat-with-an-own-client.md),
  [ADR-0098](../adr/0098-hand-values-over-from-the-chat-through-a-pinned-static-page.md),
  the spike [#255](https://github.com/igorrius/ganglion/issues/255)
- **Question:** Does the owner's first MCP server (a retailer's public
  server over Streamable HTTP with OAuth) accept the project's site as a
  redirect address, and does the whole sign-in work through a page and a
  pasted line? Does the page's WebCrypto seal a value that Go's
  `crypto/hpke` opens, in the browsers the admin uses?
- **Status:** run by hand on 2026-11-18 with a throwaway page at
  `/intake/spike/` on the project's site and a scratch Go program. The
  owner signed in with their own account; only read-only tools were
  called. No account, client id or token is recorded here.

## What was done

1. Read the server's metadata: `initialize` without a token, then the
   documents it named.
2. Registered a public client (`token_endpoint_auth_method: none`) with
   the spike page as its only redirect address.
3. The owner opened the sign-in link (PKCE S256, `state`, `resource`),
   signed in, and landed on the page; the code was exchanged for tokens.
4. `initialize`, `tools/list` and one read-only `tools/call` with the
   token.
5. The page sealed a test value in the owner's desktop browser; the line
   was pasted into a chat and opened in Go. Before that, the page's script
   was run under Node.js 26's WebCrypto and opened in Go, and a line with
   one changed character was refused.

## Findings

### The sign-in

| What | Found |
|---|---|
| Discovery | `401` with `WWW-Authenticate: Bearer resource_metadata="…/.well-known/oauth-protected-resource/mcp"`; the path-suffixed document exists, the bare one does not; one authorization server, the same origin |
| Metadata | `code` only, `query` response mode, PKCE `S256` only, `authorization_code` and `refresh_token`, `none` among the token endpoint's auth methods, `authorization_response_iss_parameter_supported: true` |
| Registration | `201`; the redirect address on the project's site was kept as given |
| The redirect | `?code=…&state=…&iss=https://<server>`: the server sends `iss` (RFC 9207) |
| The code | Opaque, about 85 characters, **with `:` in it**: the spike page's check allowed only `[A-Za-z0-9._~-]` and refused it. A line must carry the code encoded |
| Token | `200`; `bearer`; `expires_in` 30 days; a refresh token; the response repeats `resource`; `scope` empty |
| Revocation | The metadata names the token endpoint itself as `revocation_endpoint`; RFC 7009 requests there (`token`, `token_type_hint`, `client_id`) answered `200` for the refresh and the access token, and the access token then got `401`. The spike's tokens were revoked this way |

### The server

| What | Found |
|---|---|
| Transport | Plain JSON answers; no `Mcp-Session-Id` (stateless); `notifications/initialized` answered `202` |
| Capabilities | `tools` with `listChanged: true`; nothing else |
| Tools | 42, every name already prefixed with the server's own name (`<server>_find_address`) |
| Size | `tools/list` is 115 KB: descriptions up to 5,524 characters (11 over 1,000), schemas up to 3.2 KB |
| Annotations | Every tool has `readOnlyHint`; 34 read-only, 8 not, 2 `destructiveHint`; no tool places an order. One deprecated tool whose name says "update" is marked read-only (a stub that points to its successors) |
| Personal data | Several read-only tools return the account's profile, addresses, family and order history |
| A call | `tools/call` answered with one `text` content block (JSON in it) and `structuredContent` |

### The page

| What | Found |
|---|---|
| Served by the site | `200`, byte for byte the file in the repository: the site's build copies an HTML file under `docs/guide/` as it is, so the node's hash check is possible |
| Desktop Chrome 157 (Linux) | WebCrypto in a secure context; ECDH P-256 and X25519; HMAC; AES-GCM; an HPKE seal of 86 bytes for 5; the clipboard |
| Interop | The page's seal opened by `crypto/hpke` (DHKEM P-256, HKDF-SHA256, AES-128-GCM), in Node.js and from the desktop browser; a changed line fails authentication |
| Telegram's in-app browser on Android (Android 10, Chrome 157) | Every check passed: WebCrypto in a secure context, ECDH P-256 and X25519, HMAC, AES-GCM, an HPKE seal of 86 bytes, the clipboard |
| Telegram's in-app browser on iOS | **Not checked yet** |

## What changes in the decisions

1. **The `gauth1` line** carries the code and `iss` encoded:
   `gauth1.<state>.<base64url(code)>[.<base64url(iss)>]`. When the
   metadata says the server sends `iss`, the node requires it and compares
   it with the issuer (RFC 9207).
2. **The limits of R-MCP-4 do not fit a real server.** A 1,000-character
   cap on a description would skip 11 of 42 tools, the cart's main view
   among them. A tool list of 115 KB is about 30,000 tokens on every turn
   of every session. The design needs a way to keep the cost down that
   does not cut descriptions; this is a decision for the owner (below).
3. **Names.** A tool whose name starts with `<server>_` drops that prefix
   when offered as `<server>.<tool>`.
4. **`readOnlyHint` is mostly right but not always**, which the card
   already covers: the admin sees every rule before Connect.
5. **Read-only is not private.** A read-only tool can put the account's
   personal data into the model's context and so to the model's provider.
   The card should say which tools return personal data when the server
   does not mark them; at least the admin sees the list.
6. **The client** must work with no session header and with plain JSON
   answers; the event-stream path stays for other servers.

## Open for the owner

How a server with many large tools reaches the model without costing
30,000 tokens a turn:

- **A. All approved tools on every turn.** Simple; prompt caching makes
  repeated turns cheaper but the context stays full.
- **B. The admin picks the tools on the card**, the rest off. Fewer
  tokens, but the choice is made once and the agent cannot reach the rest.
- **C. On demand.** The agent sees one small tool per server that lists
  its tools by name and first line; it turns on the tools it needs for the
  session, and their full definitions join from the next step.
