# ganglion docs

The engineering record: what is decided and why. Status, plans and tasks are
not kept here: they live in
[GitHub Issues](https://github.com/igorrius/ganglion/issues) and on the
[ganglion project](https://github.com/users/igorrius/projects/13), with the
milestones of each phase
([ADR-0094](adr/0094-the-github-project-holds-the-plan-and-the-repository-holds-decisions.md)).

| Question | Read |
|---|---|
| What are we building, what is in scope now, what are the requirements, what are the phases and their gates? | [spec.md](spec.md) |
| What does the system look like, from the big picture down to packages? Where is X in the code? | [architecture/](architecture/README.md), [the code map](architecture/05-code-map.md) |
| Why was it decided this way, and what else was considered? | [adr/](adr/README.md) |
| What is the reasoning behind a larger piece of work? | [design/](design/) |
| What did we find out before deciding? | [research/](research/) |
| How do we write Go code here? | [conventions.md](conventions.md) |
| How is a core release made and its signing key kept? | [releasing.md](releasing.md) |
| How is the node deployed on Railway? | [deploy-railway.md](deploy-railway.md) |
| How do I set up the test bot, run a live check, release a module, read the docs in Obsidian? | [development.md](development.md) |
| How do I install, configure and run a node? | [guide/](guide/index.md), published at [igorrius.github.io/ganglion](https://igorrius.github.io/ganglion/) |

Ids of the form `G-<n>` in ADRs and comments are cards of the old backlog
board, which is read-only now: read them as labels, not links. Bugs found
from outside go to GitHub Issues
([CONTRIBUTING.md](https://github.com/igorrius/ganglion/blob/master/CONTRIBUTING.md)).
