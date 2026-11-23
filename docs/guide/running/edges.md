# Connecting a computer as an edge

An edge is another computer, next to your devices, that runs modules and reports to your main node. You connect one by asking in the chat: you approve a card, get a one-time code and run one command on the computer. For what an edge does once it is linked, see [Edges](operations.md#edges).

## Before you start

- Your main node has a public address, `http.addr` behind a proxy that ends TLS, and your edges' entry to the bus is on. See [the `http` section](configuration.md#http) and [the `bus` section](configuration.md#bus).
- The computer has the `ganglion` binary, the same version as your main node.
- You are the admin and talk to the bot in your private chat.

## Connect a computer

1. Tell the bot what you want, in your own words: "connect my computer as an edge named `home`". The agent uses the `edge.join` tool.
2. A card shows the edge's name and what it will be allowed to do. Press **Approve** to go on, or **Cancel**.
3. After Approve the bot shows a one-time code and the command to run, for example:

   ```sh
   ganglion edge join https://example.com <code>
   ```

   The code works once and for 10 minutes. It is shown only once and anyone who has it can join an edge under that name, so treat it like a password: do not paste it anywhere else. To keep it out of your shell history, give `-` instead of the code and type it on standard input.
4. Run the command on the computer. It makes the edge's key there (the secret half never leaves the computer), sends the code and the public half to your main node, and writes two files beside the node's database: `edge.creds`, readable only by you, and `edge.operator`. It does not overwrite them unless you add `--force`.
5. The command prints an `edge:` block for the node's settings file on that computer, and then the command to start it. Put the block in the file, for example:

   ```yaml
   edge:
     name: home
     url: wss://example.com/bus
   ```

   and run the node with `--roles edge`.
6. The bot tells you "`home` joined" in the chat on every join. If you did not expect one, revoke the edge (below).

The edge renews its credentials by itself every hour; you do nothing.

## What the permissions mean

The card lists what the edge may do on your network. In plain words, an edge may:

- send its own events and its own metrics, and nothing under another edge's name;
- call the ports of your modules on the main node that its modules are linked to, and answer calls from the main node to its own modules;
- hear nothing else on the bus.

When you link a module on the edge to a module on the main node, the edge's permissions change. The change reaches the edge within an hour, because the edge asks for fresh credentials hourly and reconnects when they differ; restarting the edge applies it at once.

## Pages on the edge

A module can serve a web page, for example a small dashboard of what it reads. A node with the edge role serves such pages on its own computer only:

- Open `http://127.0.0.1:8090/<module>/` in a browser on the edge's computer. `http://127.0.0.1:8090/` lists the pages of the modules that are running.
- Nothing else can reach it. The address is the computer's loopback, so another computer on your network gets no answer. To look at a page from elsewhere, use your own tunnel (for example `ssh -L 8090:127.0.0.1:8090 <edge>`).
- The port comes from the `http.local_addr` setting of the node that runs the module, so each edge decides its own; the default is `127.0.0.1:8090`, and `off` serves no pages. See [the `http` section](configuration.md#http).
- In the install window of a module with pages you see a line like "Serves web pages at `http://127.0.0.1:8090/<module>/`" with its routes. That address is on the computer that runs the module.

## Revoke an edge

Send `/edges` in your private chat. It lists the joined edges, each with a **Revoke** button. Revoke drops the edge's connection at once and the main node refuses its key from then on.

A lost or stolen computer: revoke it, then join a new one. The name is free again as soon as the old edge is revoked, so you can reuse it.

## When it does not work

| What you see | Why | What to do |
|---|---|---|
| The join command says the code was refused | The code was already used, was mistyped or cut short, or its 10 minutes ran out | Ask the bot for a new one; a new code for the same name replaces the old |
| The join command says too many attempts | Five wrong tries in a row lock joins for a short while, about 12 seconds per further try. This is for the whole node, so a stranger's tries can lock you out too | Wait a minute and run the command again with the same code; a failed try does not use the code up. Edges already connected are not affected |
| The join command says the permissions changed since the card | The modules or links of that computer changed after you approved the card, so the code no longer matches what you read | Ask for the edge again in the chat; the new card shows what it will get now |
| The bot says the name is taken | An edge with that name is already joined | Pick another name, or revoke the old edge in `/edges` first |
| The code expired or the bot does not know it | The 10 minutes passed, or the main node restarted: unused codes are lost on a restart | Ask the bot again |
| The command refuses to write the files | `edge.creds` is already there | Run it again with `--force` if you mean to replace it |
| The edge cannot connect after a revoke | That is the point: the key is refused for good | Join again with a new code; the edge's old files are replaced with `--force` |
| The edge cannot connect right after a join | The `edge:` block is missing or its `url` is wrong, or the main node's public address is not reachable | Check the `edge` section against what the command printed |
