# ADR-0098: Hand a code or a secret over from the chat through a pinned static page and a pasted line

- **Status:** Accepted
- **Date:** 2026-11-18
- **Deciders:** Igor Artasevych
- **Spec:** [7.17](../spec.md#717-handing-values-over-from-the-chat) (R-INT-1…8), [7.14](../spec.md#714-node-configuration) (R-CFG-9, R-CFG-11), [7.8](../spec.md#78-security-and-operations-mvp), [11](../spec.md#11-open-decisions), [14](../spec.md#14-not-now); [design note](../design/mcp-client-and-secret-intake-design.md), [earlier design note](../design/secrets-intake-design.md); [ADR-0077](0077-encrypted-secret-store-on-the-nodes-key.md), [ADR-0081](0081-update-the-node-from-signed-releases-at-the-owners-command.md), [ADR-0097](0097-connect-remote-mcp-servers-from-the-chat-with-an-own-client.md)

## Context

Two things have to reach the node from the admin's phone without a shell:

1. **An OAuth code.** Connecting a remote MCP server (ADR-0097) signs the
   admin in through a sign-in server that redirects the browser, with a
   one-time code, to an address the node registered. The node may have no
   public address: it reads Telegram by polling.
2. **A secret.** A key for a server that wants one in a header, and a
   module's secret ([#172](https://github.com/igorrius/ganglion/issues/172)).
   Today it takes `ganglion secret set` on the host (ADR-0077), which a
   person who runs a node from a template cannot do (owner, 2026-11-18).

The two differ in what a third party learns. A code is worthless without
the PKCE verifier, which never leaves the node. A secret is the value
itself.

The rules that hold: the model never sees a secret; a value never enters
the inbox, the history, the journal or the logs; the chat's history on
Telegram's servers must not hold it in clear (#172's design note).

Facts: Go's standard library has `crypto/hpke` (RFC 9180) in the
project's Go; every browser's WebCrypto has ECDH on P-256, HKDF and
AES-GCM, X25519 only in recent versions; Telegram's link buttons take
`https:` addresses, not `data:`; Telegram widgets run QuickJS without
WebCrypto and send at most 64 bytes back
([research](../research/2026-11-16-telegram-widgets.md)). The node has an
optional public listener for the webhook (R-CFG-11), off by default.

## Options

### A. The admin types the value into the chat; the transport takes it and deletes the message

No page. The value passes Telegram's servers in clear and lies in their
storage until the deletion.

### B. A static page on the project's site, versioned and pinned by the release

One HTML file in two modes: it shows an OAuth code as a line to copy, or
encrypts a secret in the browser to the request's one-time key and shows
the ciphertext as a line to copy. The admin pastes the line; the transport
takes it before the inbox. The binary embeds its copy of the page and
checks the hosted one against it before each use. The address is a
setting.

### C. A page on the owner's own host (a Cloudflare domain or Worker)

As B, on infrastructure the owner controls.

### D. The node serves the page from its public listener

As B, with no third party, for a node that has a public address.

## Comparison

| Criterion | A. Typed in chat | B. Pinned static page | C. Owner's host | D. Node serves it |
|---|---|---|---|---|
| Works with no public address | Yes | Yes | Yes | No |
| A secret in clear on Telegram's servers | Yes, until deleted | No: ciphertext to a destroyed key | No | No |
| Who can take a secret | Telegram | The site's host serving a targeted page; a maintainer's mistake is caught by the check | The owner's account; every other user depends on it; a Worker is code in the middle | The node's own host, already trusted |
| A maintainer's mistake on the site | — | Stops every node's secret input at once | Not caught unless the same check is added | — |
| Fit for an OAuth return | No redirect target | Yes | Yes | Yes |
| Effort | Smallest | A page, its check, CI that keeps versions immutable | B plus an account to run | B plus a new public route |

## Decision

**B, with D as a later step** (owner, 2026-11-18). B works for a node with
no address and keeps a secret out of the chat's history; its check turns
the owner's worry, a maintainer's mistake that replaces the page, into a
stop rather than a leak. C adds a party and does not remove the class of
risk B leaves. A leaves the value with the transport.

- **Requests** (package `intake`): an OAuth code or a secret; opened by
  the node, not the model; answerable by an admin only; 10 minutes;
  memory only, so a restart closes them. A secret request holds a
  one-time P-256 key pair; its private key is destroyed when answered or
  expired.
- **The page:** `intake/v1/index.html` on the project's site, the default
  of `mcp.page`. A published version never changes: CI checks every
  published version against `intake/published.sha256`; a new format is a
  new version, the old one stays. It sends nothing over the network
  (`default-src 'none'`, `connect-src 'none'`, inline code by hash).
- **The check:** before every secret request the node fetches the
  configured page (public address, size limit) and compares its SHA-256
  with the copy embedded in its binary. A mismatch refuses the request and
  tells the admin that secret input is stopped and why. For an OAuth
  sign-in a mismatch only adds a warning to the card.
- **The cipher:** HPKE base mode, DHKEM(P-256, HKDF-SHA256), HKDF-SHA256,
  AES-128-GCM; `info` is `ganglion-intake-v1`, the request id and the
  secret's name. A value through the page is at most 2 KB, so its line
  fits one Telegram message.
- **The lines:** `gauth1.<state>.<base64url(code)>[.<base64url(iss)>]`
  and `gsec1.<id>.<base64url(enc || ciphertext)>`. A code is opaque and
  may hold any character (the spike's server put `:` in it), so it is
  encoded; `iss` is carried when the sign-in server sends it, and then the
  node requires it and compares it with the issuer (RFC 9207). The
  format's version is in the prefix; an unknown one is refused.
- **The return channel:** pasting the line into the chat. The transport
  recognises a private text message that is one such line, takes it before
  the inbox, hands it to `intake`, and deletes the message. From someone
  who is not an admin, or in a group, it is deleted when the bot can and
  goes nowhere. Telegram's `sendData` is not used: pasting works in any
  chat and needs no keyboard button.
- **Tools:** `secret.request(name, why[, module])` for the agent; the
  card is the node's text and names the module and the hosts its manifest
  may reach, with the agent's reason quoted as data. No tool reads a value.
- **D later:** a node with `http.public_url` serving its own copy is a
  "Not now" item; it adds a route to a public entry.

This ADR settles the open questions of the earlier design note: the page
is not served by the node by default, `sendData` gives way to pasting, the
cipher suite is the one above, and a request from a tainted session asks
as usual because the card is the node's own text.

## Security

- **Trust boundaries:** a new one, the page's host, for secrets only. A
  new input path from the transport, before the inbox, for the two
  prefixes.
- **What an attacker controls:** the page's host and the site's
  repository; the chat (anyone who writes to the bot); the model, steered
  to ask for a secret; a pasted line.
- **Abuse cases and what stops them:**
  - *The page on the site is replaced* (a merged change, a stolen
    maintainer account). Every node's check fails, so no node issues a
    secret link until the page matches the node's release again.
  - *The page and a release are replaced together.* A compromised release
    is a compromised node with or without the page; releases are signed
    (ADR-0081).
  - *The host serves the node one page and the phone another.* Not
    stopped; it needs the host itself. D removes it for a node with a
    public address.
  - *A pasted line from someone else, or replayed.* Admin-only, private
    chat, one use, an open request's id; anything else is deleted and
    dropped.
  - *A code read from the chat's history or the page's server.* Useless
    without the verifier in the node's memory.
  - *A ciphertext moved to another request.* The request id and the name
    are in `info`; the key pair is one-time.
  - *The model asks for "the bank password".* The card is the node's text
    and says which module or server asks and where the value may go; the
    model's reason is marked as its own.
  - *A value in the inbox, the history, the journal or the logs.* The line
    never enters the inbox; the transport logs only the request id; a
    decrypted value goes to the store and the redactor. The leak test
    searches every one of these after a run.
- **Not protected:** a compromised phone or host; a transport that
  rewrites the link on a button (Telegram already carries the admin's
  presses).

## Consequences

- A secret and a server's sign-in need no shell and no public address.
- The project's site carries a page that every node depends on; its
  versions are frozen by CI, and a change is a new version.
- The page's script has to be tested against Go's `crypto/hpke`: CI runs
  it under Node.js's WebCrypto.
- `/login` for the claude CLI
  ([#174](https://github.com/igorrius/ganglion/issues/174)) can use the
  same requests and lines.
- A spike came first
  ([research](../research/2026-11-18-remote-mcp-and-intake-spike.md)):
  the owner's first server accepted the page as its redirect address and
  the sign-in worked end to end; the page's seal opened in Go from desktop
  Chrome and from Telegram's browser on Android. Telegram on iOS was not
  checked: no iPhone was at hand, and the owner accepted the ADR without it
  (2026-11-18). The page checks WebCrypto before it asks for a value and
  says so when it is missing; `ganglion secret set` stays the way round.

## Revisit when

- The spike fails for either question.
- Nodes with a public address are common enough that D should be the
  default.
- Telegram offers a way to hand a value to a bot that never stores it.
