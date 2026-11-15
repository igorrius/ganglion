# The project's release keys

Each `*.pem` file here is a public Ed25519 key (PKIX, `PUBLIC KEY`) that
signs the `SHA256SUMS` of a core release (R-UPD-1,
[ADR-0081](../../docs/adr/0081-update-the-node-from-signed-releases-at-the-owners-command.md)).
The node embeds every key in this folder and accepts a release that any of
them signed. A new key comes in a release signed with an old one; a key
that is dropped is removed from here.

Check a release by hand:

```sh
openssl pkeyutl -verify -pubin -inkey update/keys/<key>.pem -rawin \
  -in SHA256SUMS -sigfile SHA256SUMS.sig
sha256sum -c SHA256SUMS
```

`scripts/release-key.sh` makes a key; [releasing](../../docs/releasing.md)
says how a release is made.
