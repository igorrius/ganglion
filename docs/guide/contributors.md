# For contributors

## Architecture in short

ganglion is one Go program. In the long term it is meant to be a network of nodes with three roles: a core that holds conversations and memory, edge runners next to devices, and workers where the models run. Today one process carries all three. It talks to Telegram, calls models through a router that picks a model per purpose, keeps state in a SQLite database and plain files, and runs tools under policies.

The agent can write small modules in Rust. The node builds each to WebAssembly in a sandbox, asks the user to approve it, and runs it in a WASM runtime (wazero) on a schedule or when state cells change, with only the capabilities its manifest asks for. The model is not involved in a routine run.

## The engineering record

- [Where we are](https://github.com/igorrius/ganglion/blob/master/docs/README.md): the project's current state.
- [Specification](https://github.com/igorrius/ganglion/blob/master/docs/spec.md): what is built, in what order.
- [Architecture views](https://github.com/igorrius/ganglion/blob/master/docs/architecture/README.md): from context down to the code map.
- [Decision records](https://github.com/igorrius/ganglion/blob/master/docs/adr/README.md): why things are the way they are.
- [Conventions](https://github.com/igorrius/ganglion/blob/master/docs/conventions.md): how the code is written.

## Take part

- [CONTRIBUTING.md](https://github.com/igorrius/ganglion/blob/master/CONTRIBUTING.md): read it before a pull request.
- [SECURITY.md](https://github.com/igorrius/ganglion/blob/master/SECURITY.md): report a vulnerability privately.
- [Issues](https://github.com/igorrius/ganglion/issues): bugs and ideas.
- [Project board](https://github.com/users/igorrius/projects/13): what is in work and what comes next.
