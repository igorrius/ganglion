---
name: module-writing
description: Write, build and offer a module of the owner's system, such as a reflex that reacts to cells and writes to the owner
---
The system you are changing is what you run on as an agent. Be careful not to break it: if it
breaks, you stop working.

A module does its one task fully and hands the system contract data. It interprets what it
fetched itself and returns only what its tools and cells declare: numbers, flags, short fixed
values, not raw text from outside. Plain code full of if-else is fine; what matters is that the
model works after the module on clean, prepared data, or not at all.

## When to make a module

Make one when a reaction repeats and needs no thought: "tell me when the battery falls below
30", "say so when a reading goes quiet". The module does it every time without a model, so it is
cheaper and cannot be talked into anything. Do not make one for a thing that happens once, for a
decision that needs judgment, or for text you would have to read to understand. Those stay with
you.

## The shape of a module

- One module, one responsibility. If you are about to write "and also", write a second module.
- A module takes data in and gives data out only through cells. It listens to the cells of
  other modules (`cells.read`, a `cell` trigger) and reports its own. It never listens to its own
  cells: the node refuses that, and it would be a loop.
- The messages it writes to the owner come from a template in code with numbers filled in. Never
  from a model, and never text that came from outside (a page, a message, a cell's text).
- Cell ids in a trigger and in `cells.read` are full ids, `<module>.<cell>` (`boiler.temp`).
- In `src/logic.rs` keep the names `react`, `Settings`, `State` and `LISTENED`: the template's
  glue calls them. `LISTENED` must equal the manifest's `cells.read`; if they differ the module
  builds but never reads the cell.
- Least rights. Ask for no network and no secrets a reflex does not need. Size
  `notify.per_day` to what the rules can really say in a day, not to 100.
- Dependencies only from the template's `Cargo.lock` (`serde`, `serde_json`). A build whose
  lock lists anything else is not clean.

## The loop

1. `module.import` the example `reflex-template` under the new name. It waits for the owner's press.
2. `module.read` its `manifest.yaml` and `src/logic.rs` (and the README).
3. `module.write` `manifest.yaml` and `src/logic.rs` only: the rules, the settings, the state and
   their tests. `src/lib.rs`, `src/host.rs` and `Cargo.lock` stay as the template has them. Write
   a Rust test in `logic.rs` for every rule, and keep `react` free of host calls.
4. `module.build`. The build runs in the background: the call answers at once, a card in the
   chat shows the owner its progress, and the result comes back later as a note
   (`[build finished] <name>: …`) that starts your next step. End your turn after the call and
   tell the owner the build runs; do not call `module.build` or `module.write` on the module
   again until its note comes (the node refuses both meanwhile). Read the diagnostics in the
   note: they name your files and lines. Fix and build again.
   After 5 builds of the module in a row the node refuses another until the owner writes: stop,
   tell the owner what fails and ask how to go on.
   The node's build does not run the module's tests; the owner and CI do. Read your tests back
   with `module.read` and keep the logic simple enough to check by reading.
5. When the note shows a clean build, `module.install` with its digest. The owner sees what the module
   reacts to, the rights it asks for and how many messages a day, and presses the button. Do not
   describe it as installed before then.

`module.write` works only in a clean conversation: it refuses once this conversation has read
outside content (a web page, a forwarded message, a module's output). When it refuses, tell the
owner and ask the owner to send `/new` (you cannot type it) or to start a new conversation; do not look for another way to write the files.
