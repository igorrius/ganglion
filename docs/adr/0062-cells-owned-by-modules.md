# ADR-0062: Let modules own the cells they declare, and prefix cell and tool names with the module's name

- **Status:** Accepted
- **Date:** 2026-11-10
- **Deciders:** Igor Artasevych
- **Spec:** [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-1, R-RUN-7, R-RUN-11, R-RUN-13), [9.4](../spec.md#94-skill-compilation-p3), [9.6](../spec.md#96-flow-engine-p3); [M4 design](../plans/p1-m4-reflexes-design.md) section 7; extends [ADR-0014](0014-state-cells.md); [ADR-0041](0041-module-install-update-and-trust-boundary.md), [ADR-0049](0049-three-gaps-where-outside-text-and-private-data-cross.md), [ADR-0058](0058-load-shedding-leaves-p1.md), [ADR-0061](0061-local-modules-built-from-source.md)

## Context

ADR-0014 made cells the shared state of the node: one data model
(desired, reported, version, history), three kinds (switch, sensor,
request). M1 built them in the node's database. A cell's name is a free
string today, and nothing records who may write it.

M4 needs:
- `deye-reader` writing the battery's state of charge, the grid status
  and the whole reading into sensor cells on cron;
- the agent reading them, with history ("how did it discharge
  overnight?");
- the owner seeing them when the model is out of limits.

Later, flows link ready modules through cells (ADR-0058, spec 9.6): one
module's sensor is another's input.

The P1 plan had cells in the node config (D-3) and access granted per
cell in the manifest. Two modules could then write the same cell, and a
removed module's cells would linger in the config.

Tool names have the same problem. A manifest declares `tool: deye.status`
today; nothing stops a module from declaring a name a built-in tool or
another module already uses.

## Options

### A. Cells in the node config, access granted in the manifest

The config declares each cell with its kind. A manifest lists the cells
the module may report and read, and the install checks they exist.

### B. Cells declared in the manifest and owned by the module

The manifest declares the cells the module reports, with kind and a short
description, and the cells of other modules it reads. Install creates the
cells and records the module as their owner. The config has no cells
section.

## Comparison

| Criterion | A. Config declares | B. Module owns |
|---|---|---|
| Where to read what a module creates | Two files | Its manifest |
| Two writers of one cell | Possible | Impossible: one owner |
| What the install window shows | Grants to cells someone else declared | The cells it creates and reads |
| Work to install a module | Edit the config, then install | Install |
| A removed module's cells | Stay declared, unused | Stay, ownerless and read-only |
| Link for future flows | A grant in a manifest | `cells.read` by full name |

## Decision

**B** (owner, 2026-11-09). A module and the cells it writes come and go
together, and one owner per cell needs no rule beyond the name.

```yaml
cells:
  report:
    soc:    {kind: sensor, about: "Battery state of charge, %"}
    grid:   {kind: sensor, about: "Grid present: true/false"}
    sample: {kind: sensor, about: "The whole reading, 12 fields"}
  read: []                  # other modules' cells, by full name
```

- **Install creates the cells** `deye-reader.soc`, `deye-reader.grid`,
  `deye-reader.sample` and records the owner `local/deye-reader`. A name
  another module owns refuses the install.
- **A module names its own cell without the prefix:**
  `cell.report("soc", 78)`. The node adds `deye-reader.` and checks the
  manifest and the owner. Whatever name a module passes becomes its own
  name plus a dot, so it cannot address another module's cell for
  writing.
- **Reading another module's cell** needs it in `cells.read` by full name,
  shown in the install window. This is the link flows will use.
- **Removing a module** keeps its cells and their history without an
  owner: nobody writes them, anyone allowed reads them. Only a module of
  the same full name (`local/deye-reader`) takes them back, and its
  install window says so ("takes over 3 existing cells with their
  history").
- **Reserved names.** The prefix is the name without `local/`, so a local
  module may not be named like a built-in module (`html2md`), a built-in
  tool group (`cells`, `module`, `modules`, `memory`, `host`, `web`,
  `schedule`, `message`, `skill`, `metrics`, `config`) or a core cell
  family (`request`). The list lives in code next to the tool registry;
  the install refuses a reserved name.
- **The agent reads any cell and never writes a sensor.**
- **Only strings taint** (owner). A module's tool output and its cells'
  values are outside content only where they are strings: numbers,
  booleans and null never taint a session; any string taints it like a
  fetched page (ADR-0049, R-RUN-13). Deye's values are all numbers, so
  asking about the battery taints nothing.
- **`about` is for the owner.** It is shown in `/cells`, never to the
  model, which gets the cell's name and kind.
- **Modules hand over contract data** (owner). A module interprets what it
  fetched and returns only what its tools and cells declare: numbers,
  flags, short fixed values, not raw text from outside. The model works
  after it on prepared data, or not at all. The module-writing skill of P3
  carries this as its second mandatory instruction (spec 9.4).
- **Tools follow the same rule:** the manifest declares `tool: status`, the
  agent sees `deye-reader.status`. A module's tool collides neither with a
  built-in tool nor with another module's.
- **Host functions in M4:** `cell.report` and `cell.get`. `cell.set` (a
  switch's desired value) waits for the first module that needs it.
- **Seeing cells:**
  - `cells.list` for the agent: every cell with its name, kind, owner,
    value and age;
  - `cells.get` with an optional period returns history, condensed: a
    sample of points plus min, max and mean, so a night of readings does
    not flood the context;
  - `/cells` for the owner, without a model: values with their age, and
    under each cell its `about` and a button with its latest changes.

### The contract is checked (owner, 2026-11-09)

The manifest declares the shape of every tool's output and the type of
every cell's value (number, boolean, a string from a fixed list, or a
free string; objects of these). The node checks each answer and each
`cell.report` against it. A mismatch is a format error: the answer never
reaches the model or the cell, the run counts as a failure, and
`module.format` is raised. The agent receives the node's own description
of the mismatch (`deye-reader.status: field soc: expected number, got
string`), never the value, as context to revise and improve the module.
Modules are written in strictly typed languages, so a mismatch is a bug
to fix, not data to tolerate.

## Security

- **Trust boundaries:** between modules that share the node's cells, and
  between what a module reports and the agent that reads it.
- **What an attacker controls:** a module's code and manifest (ADR-0061),
  and so the names it passes to the host functions and the values it
  reports.
- **Abuse cases and what stops them:**
  - *A module writes another module's cell* (`cell.report("other.soc")`,
    `"../soc"`). The node prefixes every name with the module's own and
    refuses a name not in the manifest's `report` list.
  - *A module claims a cell at install.* The install is refused if another
    module owns the name.
  - *A module reads cells it did not declare.* `cell.get` answers only for
    its own cells and the full names in `cells.read`, which the owner saw
    in the window.
  - *A module's tool shadows a built-in tool or another module's.* Its
    tools are named `<module>.<tool>` by the node, not by the manifest, and
    a module cannot take a reserved name (a built-in module, a tool group,
    `request`).
  - *A module takes over a removed module's cells and history.* Only a
    module of the same full name takes them back, and the window says so.
  - *A module plants instructions for the model* in a cell value, a tool
    result or a cell's `about`. A string value taints the session like a
    fetched page, so the policies of ADR-0049 hold; `about` never reaches
    the model.
  - *The agent, talked into it by an injected page, forges a reading.*
    No tool writes a sensor.
  - *A module returns text where its contract promised a number.* The
    node checks the answer against the manifest and drops it as a format
    error; the agent learns the field and the expected type, not the
    text.
  - *A module floods the history to fill the agent's context.* `cells.get`
    condenses history; the turn sees a sample and the summary figures.

## Consequences

- The node config has no cells section; D-3's "cells" moves to the
  manifests ([ADR-0059](0059-node-config-file.md)).
- The manifest schema gains `cells` and short tool names; `deye.status`
  becomes `deye-reader.status`.
- The cell store records an owner per cell; `cell.report` checks it.
- A cell outlives its module. History is kept, as ADR-0014 says.
- Flows (ADR-0058) link modules by full cell names in `cells.read`.
- A new built-in module or tool group adds its name to the reserved list;
  a local module that already uses the name has to be renamed.
- The taint rule rewards modules that return numbers and flags: such a
  module keeps a session clean, and the stricter rules for a tainted
  session (ADR-0049) do not step in. A module that returns text taints the
  session.
- The exact shape of the condensed history is left to the build.

## Revisit when

- The flow engine starts (spec 9.6): `cell.set`, switches written by
  modules, and links between modules.
- A cell needs two writers.
- A fourth cell kind or nested state appears (ADR-0014).
