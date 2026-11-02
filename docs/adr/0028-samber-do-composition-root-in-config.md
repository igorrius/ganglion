# ADR-0028: Assemble the node in a `config` package of `samber/do` providers

- **Status:** Accepted; the rule on `Must*` superseded by [ADR-0030](0030-mustinvoke-inside-providers.md)
- **Date:** 2026-11-02
- **Deciders:** Igor Artasevych
- **Spec:** [2, principles 5, 7 and 8](../spec.md#2-principles), [5.1](../spec.md#51-roles-not-machines); [ADR-0004](0004-one-binary-with-roles.md), [ADR-0027](0027-written-go-conventions-and-lint-guardrails.md)

## Context

The `node` package wires the core role by hand. `runCore` and
`buildHandler` do several jobs in about 200 lines:
- open the lock and the database;
- create the queue, the access list, the sender and the transport;
- choose provider implementations in a `switch`;
- build the router and the agent;
- add the typing hook;
- start everything;
- close resources in a chain of `defer`s.

Every milestone grows these functions:
- M3b adds memory, the tool pool, policies and skills;
- M4 adds the runner, the scheduler, modules, cells and the node config
  (D-3);
- P2 adds the edge and worker roles.

The order of construction and shutdown is implicit in the code order.

The owner assembles their other Go services this way:
- a `config` package holds one provider function per component;
- each provider returns a generic `do.Provider[T]`, a function that gets
  its dependencies from the container;
- the whole application is assembled in that one package.

Constraints:
- Domain packages stay embeddable as a library with plain constructors
  (principle 5).
- A dependency must earn its place (principle 7) and its memory
  (principle 8).

## Options

### A. Manual wiring, split into functions

Keep today's approach, split `runCore` into one builder function per
component, and pass dependencies explicitly. Construction errors are
caught by the compiler. Shutdown order and lazy construction stay
hand-written, and per-role assembly is a set of `if`s.

### B. `google/wire`

Compile-time dependency injection by code generation: provider sets are
declared, and `wire` generates the wiring code. Missing providers fail at
generate time. It adds a generation step to every change, and it has no
lifecycle: shutdown order stays manual.

### C. `uber/fx`

An application framework on top of `uber/dig`. It works by reflection and
has lifecycle hooks (`OnStart`, `OnStop`). It brings a sizeable dependency
tree and owns the program's start and stop, so `main` and the components
take fx's shape.

### D. `samber/do` v2

A container built on generics:
- `do.Provider[T]` is `func(do.Injector) (T, error)`;
- providers are lazy by default and grouped with `do.Package`;
- services that implement `Shutdown` are closed in reverse dependency
  order;
- there are health checks, named services and scopes.

v2.1.0 is from 2026-07-20, with one small runtime dependency
(`samber/go-type-to-string`). A missing provider is found at run time,
when the service is invoked.

## Comparison

| Criterion | A. Manual | B. wire | C. fx | D. do v2 |
|---|---|---|---|---|
| Matches the owner's way of assembling services | No | Partly | No | Yes |
| Missing dependency found | Compile time | Generate time | Start | Start (and a test that builds every role) |
| Shutdown in reverse dependency order | By hand | By hand | Yes | Yes |
| Per-role assembly (`--roles`) | `if`s | Several injectors | Modules | `do.Package` per role |
| Domain packages stay free of the container | Yes | Yes | Mostly | Yes, enforced by `depguard` |
| Extra build step | No | Yes | No | No |
| Dependencies and memory | None | Generator only | Large tree, reflection | One small module |

## Decision

**D**, with these rules:

- **Only `config` imports `do`.**
  - `config` holds one provider per component, as a function that returns
    `do.Provider[T]`.
  - Domain packages keep plain constructors with explicit arguments.
  - `depguard` fails CI if any other package imports `github.com/samber/do`.

  ```go
  // Queue provides the SQLite inbox and outbox.
  func Queue() do.Provider[*sqlqueue.Queue] {
  	return func(i do.Injector) (*sqlqueue.Queue, error) {
  		db, err := do.Invoke[*sql.DB](i)
  		if err != nil {
  			return nil, err
  		}
  		return sqlqueue.New(startup(i), db)
  	}
  }

  // Core is the core role.
  var Core = do.Package(
  	do.Lazy(Lock()), do.Lazy(DB()), do.Lazy(Queue()), do.Lazy(Access()),
  	do.Lazy(Sender()), do.Lazy(Telegram()), do.Lazy(Agent()), do.Lazy(Loop()),
  	do.Lazy(CoreComponents()),
  )
  ```

- **Single-implementation components are provided as concrete types.**
  - A component with one implementation, such as `*sqlqueue.Queue`, is
    registered as its concrete type.
  - The constructors that consume it take interfaces (`queue.Queue`,
    `queue.Outbox`), and Go converts at the call.
- **Extension points are provided as interfaces, and their provider
  chooses the implementation at run time.**
  - Each implementation has its own lazy provider.
  - The provider of the interface reads the settings and invokes the one
    it needs, so the others are never built.
  - A no-op implementation is one of the choices. Domain code never checks
    a dependency for `nil`.

  ```go
  // Handler is the turn handler: the agent when a model catalogue is
  // set, the echo bot otherwise.
  func Handler() do.Provider[core.Handler] {
  	return func(i do.Injector) (core.Handler, error) {
  		b, err := do.Invoke[Bootstrap](i)
  		if err != nil {
  			return nil, err
  		}
  		if b.ModelsFile == "" {
  			return core.EchoHandler, nil
  		}
  		ag, err := do.Invoke[*agent.Agent](i)
  		if err != nil {
  			return nil, err
  		}
  		return ag.Handle, nil
  	}
  }
  ```

  The same choice exists today as `if` and `nil` checks in several places:
  - the handler (the agent or the echo);
  - the Telegram transport and its typing hook (on only with a token);
  - the session archive (`nil` means no archive);
  - telemetry (`off`).

  M3b and M4 add more: memory stores, policies, cell backends (`virtual`
  or `tuya`).
- **Inputs are values.**
  - The command line puts the bootstrap settings and the startup context
    into the container as values (ADR-0029).
  - The startup context is for start-time work only (migrations).
  - Providers return errors; `Must*` calls are for tests.
- **Lifecycle.**
  - Resources implement `Shutdown(ctx) error` and are closed by the
    container in reverse dependency order: the lock, the database,
    telemetry.
  - Long-running parts are components with `Run(ctx) error`, which `node`
    runs together.
  - `node` keeps the role runner and loses the assembly.
- **Safety net.** A test in `config` builds every role's container against
  fakes (the fake Bot API, `providertest`) and invokes the components, so a
  missing provider fails CI rather than a start on the host.

## Consequences

- `runCore` and `buildHandler` move into `config` as small providers.
  Adding a component means one provider and one line in its role's
  package.
- The provider `switch` becomes a table from provider type to a factory in
  `config`.
- Optional collaborators get no-op implementations instead of `nil`, so
  checks like `if a.d.Archive == nil` and `if tr != nil` leave the domain
  code.
- A wiring mistake is a run-time error. The role-building test and the
  error at start (which names the missing type) are the price.
- The code map and the dependency diagram gain `config`. The rule "only
  `cmd/ganglion` and `node` wire concrete implementations" becomes "only
  `config`".
- The binary size and the RSS after 1,000 turns are measured before and
  after, as with every dependency (principle 8).

## Revisit when

- Wiring errors reach a host despite the role test.
- `do` v2 stops being maintained, or a release breaks the provider API.
- Assembly needs something the container cannot express cleanly, such as
  runtime reconfiguration in M6.
