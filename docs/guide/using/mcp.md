# Connecting an MCP server

An MCP server is an outside service that offers tools over the web: a shop, a calendar, a ticket tracker. Once you connect one, the agent can use its tools next to its own. You connect a server from your private chat; you never edit a file for it.

Everything a server returns is treated as outside content, like a web page: it cannot give the agent orders.

## Connect a server

Ask in words, with the server's address and a short name for it:

```text
Connect the MCP server at https://mcp.example.com/mcp, call it shop.
```

The name is lowercase letters, digits and underscores, up to 24 characters. The address must be `https` and public.

If the conversation holds content from outside (a web page, a forwarded message), the node first asks you to confirm the address, since such content could have asked for an address that carries your data out. In a clean conversation it goes ahead at once.

What happens next depends on the server.

### A server that asks you to sign in

1. The assistant sends a **sign-in card**: the server's host, the host of its sign-in page, and a **Sign in** link in the text. The link works for 10 minutes.
2. Open the link on your phone and sign in.
3. The sign-in ends on a page of the project's site that shows a line starting with `gauth1.`, with a **Copy** button.
4. Paste that line into the chat as a message of its own. The node takes it before the assistant sees it and deletes the message at once. Each line works once.
5. The node lists the server's tools and sends the **tools card**.

If the card carries a warning that the page did not pass the node's check, the sign-in still works, since the line is useless without a key only the node holds, but look at the `mcp.page` setting below.

### A server that needs no sign-in

The node lists the tools and sends the tools card at once.

### A server that wants a key in a header

Some servers offer no sign-in and want a key (an API key) in a header instead. When the server refuses the node and offers no sign-in, the assistant reads the server's documentation for the header's name and asks again with it, for example:

```text
Connect https://mcp.example.com/mcp as shop; it takes the key in the header X-API-Key.
```

1. You get a **key card**: the server, the header, and the one host the key will go to, with an **Enter the value** link.
2. Open the link, type the key, copy the line the page shows and paste it here as a message of its own. The key is encrypted on your phone; the assistant never sees it, and the message is deleted at once. See [Entering a secret](secrets.md).
3. Once the key is stored, the node lists the server's tools with it and sends the **tools card**.

Never type the key into the chat itself, or give it to the assistant: it would be in the conversation. The header's name is letters, digits, `_`, `.` and `-`, starting with a letter; `Authorization` and the headers the node sets itself cannot be used.

## The tools card

The card lists the server's tools in groups, in the server's own words:

- **Run by themselves, although the server does not mark them read-only**: tools that will act without asking and that the server does not call read-only. This group comes first and always names every tool in it.
- **Run by themselves (the server marks them read-only)**: tools that only read.
- **Ask you first**: tools that change something. Each call shows a question with Approve and Deny.
- **Never offered**: tools the node will not give the agent: a count, with the names when there are few.
- **Not taken**: tools past a limit, or with a name that clashes.

Press **Connect** to turn the tools on with these rules, **Ask for all** to make every tool ask you first (it then connects), or **Cancel** to drop the server: for one not connected yet, the sign-in and registration are removed.

A server you approved before and connect again keeps the rules you set. A name keeps one address: to move a server to another address, disconnect it with `/mcp` first, or connect the new one under another name.

A card's buttons work only while the server's entry is as it was when the card came. After a newer card is approved, a rule changes or the server is disconnected, an older card says it is out of date and changes nothing.

## Say which tools you need

You do not have to go through a long list. Say it when you ask to connect: "Connect the shop, I only need the purchase history and current prices". The assistant passes that along, and the tools card already shows it: the tools you named as you said, the rest never offered. One press of **Connect** applies the whole card. If the assistant names a tool the server does not have, the node refuses it before any card and says which name.

A tool the server adds later is proposed in the usual way on the next card (read-only by themselves, the rest asking), not as "the rest".

## Change a rule

Ask in words: "let `shop.search_products` run by itself", "make `shop.add_to_cart` ask me first", "never offer `shop.place_order`". Several changes at once ("only these two, nothing else") are one question and one press: the assistant asks you to confirm once, listing what changes, for example "Change the rules of 2 tools of `shop`?" followed by each tool with the old and the new rule. When many tools change, the question names those that will run by themselves and counts the rest.

## What the assistant learns about a server

Some servers explain how their tools should be used. The assistant reads that explanation when it first turns on one of the server's tools in a conversation. It treats the text as data, the same way as what the server's tools return, never as orders.

When the assistant works out something about a server's data that will help next time, for example that a receipt's total is before discounts, it offers to remember it. Since the fact came from the server, you confirm it first. It is kept in memory under the topic `mcp-<server>`. `/memory mcp-<server>` shows it, and disconnecting the server forgets it.

## `/mcp`

`/mcp` in your private chat lists each server with its state, the time of its last sign-in, its number of tools and a **Disconnect** button. Disconnect removes the server's entry, its sign-in, its tokens and what the assistant remembered about it.

| State | What it means | What to do |
|---|---|---|
| connecting | The node is reaching the server. | Wait. |
| connected | Its tools are on. | Nothing. |
| unreachable | The node cannot reach the server. | The node tries again; check the server. |
| its tools changed | The server's list of tools differs from the one you approved, so its tools are off. | Ask the assistant to connect it again and approve the new list. |
| needs a new sign-in | The sign-in expired or was revoked. | Ask the assistant to connect it again. |
| the secret store is locked | The node's master key does not fit its database. | See [Configure](../getting-started/configure.md#the-master-key). |

`/status` shows one line for each server that is not connected.

## What lands in the file

Each connected server is a line of the `mcp:` section of `ganglion.yaml`:

```yaml
mcp:
  page: https://igorrius.github.io/ganglion/intake/v1/
  servers:
    shop:
      url: https://mcp.example.com/mcp
      auth: oauth
      tools:
        search_products: auto
        place_order: confirm
      fingerprint: sha256:…   # written by Connect
```

- `page` is the address of the intake page the sign-in ends on. The default is the project's own; leave it alone unless you host a copy.
- `auth` is `none`, `oauth` or `header`.
- `timeout` appears only when it is not the default of 30 seconds.
- `fingerprint` pins the tool list you approved.

Every change to the section is recorded in the node's file history, like any other: "mcp server shop: connected", "approved again", a tool's new rule, "disconnected".

Only these tools and cards change the section: `config.propose` refuses it. If you edit the file on the host, the node applies the edit at the next start.

## Where things are kept

The sign-in and its tokens, or a server's key, live in the node's secret store, under the server's own scope (a key under the header's name). Disconnect removes them. The assistant never sees them, and neither does the chat: the line you paste is deleted and works once. `/secrets` lists names and dates, never values.

## When something goes wrong

- **Unreachable**: the server is down or its address changed. The node keeps the entry and tries again.
- **Needs a new sign-in**: ask the assistant to connect the server again and go through the sign-in.
- **Its tools changed**: the tools are off until you approve the new list. Ask to connect the server again.
- **The sign-in link expired** (10 minutes): ask to connect again for a new card.
- **Signed in, but the tools could not be listed**: ask to connect again later.
