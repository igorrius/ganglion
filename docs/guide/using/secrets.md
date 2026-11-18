# Entering a secret from the chat

Some things your node needs are secrets: a module's API key, a Wi-Fi password, the key of a service the agent connects to. You can hand one to the node from your phone, in your private chat with the bot, without a shell on the host and without a redeploy. The value never appears in the chat in the clear, and the agent never sees it.

## How it goes

1. The agent finds that something needs a secret, for example a module whose manifest names `<NAME>` and which cannot be installed while it is unset. It asks the node for it, and tells you why.
2. The node sends you a card in your private chat. The card is written by the node, not by the agent. It names:
    - the secret, `<NAME>`;
    - who asks for it: a module, the node itself, or a server the agent connects to;
    - where the value may go: for a module, the hosts its manifest lets it reach; for a server, its address;
    - the agent's reason, quoted as its own words. The node does not check it.

    If the conversation holds content from outside (a web page, a module's output), the card says so. Make sure you asked for this.
3. Tap **Enter the value**. A page opens in your browser. Type the value and tap the button. The page encrypts the value on your phone and shows a line that starts with `gsec1.`, with a **Copy** button.
4. Copy the line and paste it into the chat with the bot, as a message of its own: no other text, no file, not forwarded.
5. The bot deletes your message, opens the line and stores the value in the node's encrypted store. The card turns into one line: `<NAME>` is set, with the time. The agent is told that it is set, never what it is.

A module reads the new value the next time it asks for it. Nothing restarts. The node's own secrets (a `store:<NAME>` in its file, such as a model key) are read at start: restart the node with `/restart` after setting one.

## Rules of the link

- The link works once and for 10 minutes. A restart of the node closes it. Ask the agent again for a new one.
- Only an admin can answer, and only in the private chat. The same line pasted in a group, or by someone else, is deleted and goes nowhere.
- A line that was already used, expired or damaged gets a short answer from the bot saying so.
- The page sends nothing over the network. It is a fixed page on the project's site, and your node checks before every request that the page there is exactly the copy built into your node's version. If it is not, the node does not ask, and tells you that handing secrets over from the chat is stopped. Use the host instead (below).
- The page takes a value of up to 2 KB.

## On the host instead

The command line on the host works for any secret, and is the way for a value over 2 KB:

```bash
printf %s "$VALUE" | ganglion secret set --module <module> <NAME>
```

Leave out `--module` for the node's own secret. See [Configure](../getting-started/configure.md#secrets).

## What never to do

Never type a secret into the chat as plain text, and never paste it to the agent. If you did, change it at its source: the chat's history and the agent's conversation may keep it.

## See what is stored

`/secrets` lists the stored secrets by name, with the date and who set it. It never shows a value. `/secrets rm <module or node> <NAME>` removes one.

## If it does not work

- **The card says the store is locked.** The node's master key does not fit the stored secrets. See [Troubleshooting](../troubleshooting.md#secrets-read-as-unset).
- **The bot says the line was not accepted.** The line was damaged in copying, or it was made for another request. Ask again and use the new link.
- **The agent says the module's manifest does not list the secret.** A module may only be given a secret its manifest names under `secret.get`. The agent adds it to the manifest first, and the install window shows it to you.
