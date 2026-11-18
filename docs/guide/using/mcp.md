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

Not available from the chat yet: entering a key in the chat waits for the node's secret entry. The assistant tells you the server wants a key.

## The tools card

The card lists the server's tools in groups, in the server's own words:

- **Run by themselves**: tools that only read.
- **Ask you first**: tools that change something. Each call shows a question with Approve and Deny.
- **Never offered**: tools the node will not give the agent.
- **Not taken**: tools past a limit, or with a name that clashes.

Press **Connect** to turn the tools on with these rules, **Ask for all** to make every tool ask you first (it then connects), or **Cancel** to drop the server: for one not connected yet, the sign-in and registration are removed.

A server you approved before and connect again keeps the rules you set.

## Change a rule

Ask in words: "let `shop.search_products` run by itself", "make `shop.add_to_cart` ask me first", "never offer `shop.place_order`". The assistant asks you to confirm, with the old and the new rule: "Change the rule of `shop.x` from «asks you first» to «runs by itself»?".

## `/mcp`

`/mcp` in your private chat lists each server with its state, the time of its last sign-in, its number of tools and a **Disconnect** button. Disconnect removes the server's entry, its sign-in and its tokens.

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

The sign-in and its tokens live in the node's secret store, under the server's own scope. The assistant never sees them, and neither does the chat: the line you paste is deleted and works once. `/secrets` lists names and dates, never values.

## When something goes wrong

- **Unreachable**: the server is down or its address changed. The node keeps the entry and tries again.
- **Needs a new sign-in**: ask the assistant to connect the server again and go through the sign-in.
- **Its tools changed**: the tools are off until you approve the new list. Ask to connect the server again.
- **The sign-in link expired** (10 minutes): ask to connect again for a new card.
- **Signed in, but the tools could not be listed**: ask to connect again later.
