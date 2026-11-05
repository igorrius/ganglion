# Agent-managed skills: current practice and the attacks it opened

- **Date:** 2026-11-05
- **Question:** how do current agent products let the agent create and manage its
  own skills, what attacks did this open, and what evidence supports each way of
  accepting a skill?
- **Status:** research input for a design brainstorm; no design chosen. It feeds
  the "Not now" entry in [spec](../spec.md) and may lead to a change of R-RTR-4 and
  [ADR-0042](../adr/0042-skills-and-scripts-before-the-sandbox.md).

## How far to trust this note

Marks: **[L]** read directly in the local OpenClaw 2026.10.3 docs; **[V]** read in
the primary source through a fetch tool that summarises with a small model, so check
a figure before quoting it in an ADR; **[A]** arXiv abstract only; **[2]** second-hand.
Repositories were read on `main` on 2026-11-05, unpinned. Papers are mostly unreviewed
preprints, and vendor posts sell products.

## Where ganglion stands

Only the owner installs a skill, and no tool writes there (R-RTR-4). Scripts declare
`effects`; the sandbox comes in M3d (R-RTR-5). The memory guard is a cheap clean-context
model that fails closed ([ADR-0034](../adr/0034-clean-context-guard.md)). General threats
are in [the baseline note](2026-11-02-agent-security-baseline.md) and are not repeated.

## 1. The Agent Skills format

**What the spec fixes** (https://agentskills.io/specification) [V]:

- A skill is a directory with `SKILL.md`, plus any other files; `scripts/`,
  `references/` and `assets/` are conventions.
- Required: `name` (1-64 characters, `a-z0-9-`, no leading, trailing or double
  hyphen, must match the directory) and `description` (1-1024 characters).
- Optional: `license`, `compatibility` (up to 500 characters), `metadata` (string
  to string, the place for client-specific properties), and `allowed-tools`
  (experimental, support varies).
- The body has no format restrictions.
- It says nothing about authorship, provenance, signing, review or updates.

**Progressive disclosure** [V]: metadata of about 100 tokens at startup; a body
under 5,000 tokens and 500 lines (recommended) on activation; other files as
needed. Anthropic gives the same levels and says a script's code does not enter
context, only its output
(https://platform.claude.com/docs/en/agents-and-tools/agent-skills/overview).

So a description is always in the prompt and steers selection, and a script's
source can go unread by the model that runs it. Both matter below.

**Validators:** `skills-ref validate` checks frontmatter and naming only, and its README
says "demonstration purposes only"
(https://raw.githubusercontent.com/agentskills/agentskills/main/skills-ref/README.md) [V].
Anthropic also refuses reserved words and XML tags in `name` and `description`, and
extensions fail upload with "Unexpected key(s)"
(overview above; https://code.claude.com/docs/en/skills) [V]. No validator found looks at safety.

**Where ganglion's format differs** (`skills/skills.go`, read 2026-11-05):

| Point | Agent Skills | ganglion |
|---|---|---|
| `name` | 1-64, `a-z0-9-` | `^[a-z][a-z0-9-]{1,31}$`: 2-32, starts with a letter; double and trailing hyphens are not refused by this pattern |
| Unknown keys | Six named fields; client-specific properties go in `metadata` | `KnownFields(true)`, and `license`, `compatibility`, `metadata` and `allowed-tools` are not in the struct, so a standard skill using them does not parse |
| Model routing | None | Top-level `model` block, a non-portable key |
| Scripts | Files in `scripts/`, run by the agent at will | Top-level `script: {run, effects, env, timeout}`, a contract enforced in code; no equivalent in the spec |
| `allowed-tools` | Experimental grant | Absent; rights come from policy |

A skill can be more than text: in Claude Code, `hooks` run for the rest of the session and
`` !`command` `` runs a shell command before the content reaches the model (page above) [V].

## 2. Neighbours' learning loops

| | Hermes Agent | OpenClaw Workshop | Claude Code `skill-creator` | Letta Code | Codex | Cursor | Goose |
|---|---|---|---|---|---|---|---|
| **Trigger** | System prompt, `/learn`, and a background review after 10 tool iterations | Foreground repair; review after 10+ model iterations; weekly review; `/learn` | User asks | User asks; README claims "skill learning" | User asks (`$skill-creator`, Record & Replay) | User asks (`/create-skill`, `/migrate-to-skills`) | No creation path found |
| **Default acceptance** | None: the agent "writes skills freely" | None: `auto` mode edits directly | The user, in a review viewer; files are written by the agent's normal tools | Not documented | Not documented | Output in `.cursor/skills/` "for review" on migration | Not found |
| **Optional gate** | `write_approval: true` stages for `/skills approve` | `propose` mode, `approvalPolicy: pending` | Permissions of the session | Not documented | `allow_implicit_invocation: false` limits use, not creation | `disable-model-invocation` limits use | Not found |
| **Scan of own skills** | Regex scan, off by default (`guard_agent_created`) | Proposals only: critical findings block apply; direct `auto` edits have no scanner | None | None documented | None documented | None documented | None documented |
| **Edit** | `skill_manage` patch or rewrite | Proposal with hash binding, or plain file tools in `auto` | Plain files | Files in a git-backed memory directory | Plain files | Plain files | Plain files |
| **Version and rollback** | Curator snapshots before consolidation (default 2 kept), ledger; agent edits outside that are not documented as reversible | Proposals keep rollback data; direct edits have no snapshot, by design | The user's git; `old_skill` snapshot for comparison | git-backed | The user's | The user's | The user's |
| **Retire** | Curator: stale at 14 days, archive at 30, never deletes | Weekly review may remove; "absence of use" alone does not justify it | Manual | Not documented | `enabled = false` | Manual | Manual |
| **Scripts** | Yes (`scripts/` writable by the agent) | Yes (support files) | Yes | Not checked | Yes; "instruction-only is the default" | Yes | Supporting files |
| **Model cost** | Fork of up to 16 iterations, on the main model by default; consolidation 50-100 API calls | One extra run, several requests; weekly run | Double runs per test case, grader; 20 queries × 3 runs × up to 5 iterations for the description | Unknown | Unknown | Unknown | Unknown |

Sources and detail for the table:

**Hermes Agent** (https://hermes-agent.nousresearch.com/docs/user-guide/features/skills,
https://hermes-agent.nousresearch.com/docs/user-guide/features/curator,
`tools/skill_manager_tool.py`, `tools/skills_guard.py`, `agent/background_review.py`,
`agent/agent_init.py` in https://github.com/NousResearch/hermes-agent) [V]:

- Trigger: `skills.creation_nudge_interval` defaults to 10 in `agent_init.py`
  (second-hand figures of 5, 3 and 15 did not match the source). The docs give no
  number.
- The review prompt pushes toward writing: "most sessions produce at least one
  skill update... A pass that does nothing is a missed learning opportunity."
- The fork replays the conversation under a tool whitelist, with
  `_REVIEW_MAX_ITERATIONS = 16`. A search of `background_review.py` and a summary
  of `skill_manager_tool.py` found **no taint or outside-content check**. That is a
  search result, not a proof.
- `skills_guard.py` is static regex, about 130 patterns plus structural checks. For
  agent-created skills a "dangerous" verdict means "ask", surfaced to the agent as
  an error asking it to retry without the flagged content. So a block teaches the
  writer what to reword.
- The docstring explains the off-by-default: "opt-in — terminal() runs the same
  code ungated". The argument is honest: while the agent has a shell, gating a
  skill write protects little. If the scan throws, the write proceeds (fails open).

**OpenClaw** ([L], `docs/tools/skill-workshop/*`, `docs/tools/self-learning.md`,
online at https://docs.openclaw.ai/tools/skills):

- Has two paths with different safety, and the safer one is not the default:
  - **Proposal path:** a `PROPOSAL.md`, "apply is the only live write", no
    clobbering, hash-bound updates that go `stale`, a scanner at apply, rollback
    metadata, states including `quarantined`, optional plugin evaluators
    (`skill_proposal_evaluate`).
  - **`auto` path, the default:** "Direct maintenance does not create proposals,
    run a post-turn scanner, or record automatic rollback snapshots."
- `approvalPolicy` defaults to `auto`: the agent itself may call `apply`.
- A heading in the docs reads "Why auto is safe to default"; the reasons are
  directory ownership and existing permissions, and it allows that "an agent can
  make an incorrect edit". **Injection through the reviewed conversation is not
  discussed in that section.** Cron, heartbeat and subagent runs are not reviewed.
- "Prompt-related keywords are not scanner findings: mentioning hidden instructions
  or tool approval does not establish an instruction override." So the scanner
  deliberately does not judge instruction text.
- Workshop skills sit below workspace skills in precedence and stay in their own directory.
- Reviewer's abstain list is useful as a filter: routine work, one-time requests,
  personal facts, transient failures, generic advice, unsupported negative claims,
  secrets.

**Claude Code `skill-creator`**
(https://raw.githubusercontent.com/anthropics/skills/main/skills/skill-creator/SKILL.md) [V]:
a guided authoring loop, not self-learning. Each of 2-3 test prompts runs with and
without the skill (or against a snapshot of the old version), a grader checks
assertions with evidence, and a benchmark gives pass rate, time and tokens with a
delta. A person reviews outputs and feedback. Description tuning uses 20 queries,
a 60/40 train-test split, 3 runs per query, up to 5 iterations, choosing by test
score. It measures usefulness and triggering; **nothing in it is a security check.**
Claude Code picks up skill file changes within a session (Claude Code page above)
[V].

**Letta Code** (https://docs.letta.com/letta-agent/skills) [V]: agent-scoped skills live in a
git-backed memory directory; the docs warn skills "can contain malware and dangerous
prompts". An automatic trigger could not be confirmed (README via search only).

**Codex** (https://learn.chatgpt.com/docs/build-skills) [V], **Cursor**
(https://cursor.com/docs/context/skills) [V], **Goose**
(https://goose-docs.ai/docs/guides/context-engineering/using-skills) [2, page not opened]:
no autonomous creation documented; absence from a page is not proof.

**Anthropic's enterprise guide**
(https://platform.claude.com/docs/en/agents-and-tools/agent-skills/enterprise) [V], for human
authors: "Skill authors should not be their own reviewers"; 3-5 eval queries including
negatives; coexistence tests; pinned versions; checksums. Its content scan blocks a failed or
unfinished scan and "complements, but doesn't replace" the checklist; its method is not described.

**Pattern across products.** The products that write skills unattended (Hermes,
OpenClaw) default to no gate. The products with a person in the loop do not learn
on their own. No product was found that combines automatic drafting, a taint rule
and required acceptance by default.

## 3. Attacks

### Marketplace campaigns

- **ClawHavoc, about 2026-02-01 to 02-03.** Koi Security audited 2,857 ClawHub skills
  and flagged 341; 335 used a fake "Prerequisites" step leading to Atomic Stealer on
  macOS (a paste from `glot[.]io`), with password-protected archives on Windows.
  Others hid reverse shells or sent `~/.clawdbot/.env` to a webhook. Publishing
  required only a GitHub account a week old. [2]
  https://thehackernews.com/2026/02/researchers-find-341-malicious-clawhub.html.
  Koi's original URL now redirects to a Palo Alto product page and could not be read.
  A later figure of 1,184 skills appears only second-hand, via the CSA note below.
- **Snyk ToxicSkills, 2026-02-05.** 3,984 skills from ClawHub and skills.sh: 534
  (13.4%) with a critical issue, 1,467 (36.82%) with any flaw, 76 confirmed malicious,
  8 still live at publication. 91% of the confirmed ones also used prompt injection.
  Skills can "poison agent memory for persistence", so it advises checking `SOUL.md`
  and `MEMORY.md` [V] https://snyk.io/blog/toxicskills-malicious-ai-agent-skills-clawhub/.
  The title's "1467 malicious payloads" is looser than its own data.
- **Scanning arrives, 2026-02-07.** ClawHub added VirusTotal Code Insight
  (Gemini-powered, so model-based), daily rescans, and a block on "malicious". The
  post itself says "this is not a silver bullet" and that a crafted injection "won't
  show up in a threat database" [V] https://openclaw.ai/blog/virustotal-partnership.
- **Unit 42, 2026-06-23: after scanning.** Five unblocked skills in February-May. One
  (`omnicogg`) put about 22 MB of padding after an AMOS payload to pass size limits:
  VirusTotal clean, ClawScan "in review", overall pass. Another listing got "Pass"
  with a verbatim paste-site lure. Two carried no malware at all: `money-radar`
  fetched affiliate links on each use, and `letssendit` told agents to pool funds
  for a pump-and-dump [V] https://unit42.paloaltonetworks.com/openclaw-ai-supply-chain-risk/.
  The CSA note names a `brand-landingpage` skill for the padding; Unit 42's text names
  `omnicogg`, so the CSA detail looks wrong
  (https://labs.cloudsecurityalliance.org/research/csa-research-note-ai-agent-skill-scanner-bypass-20260629-csa/).
- **Unit 42, 2026-06-11.** 49,943 skills, declared capabilities against behaviour: 80%
  have a mismatch, of which most classified deviations are oversight (81.1%), not
  malice. 5% (2,490) show multi-stage chains. Static and classifier-predicted, not
  runtime-confirmed [V] https://unit42.paloaltonetworks.com/ai-agent-supply-chain-risks/.

### Prompt injection through skills

- **Skill-Inject, 2026-02.** 202 injection-task pairs, up to 80% attack success on
  frontier models; argues filtering and bigger models will not solve it [A]
  https://arxiv.org/abs/2602.20156.
- **DDIPE, 2026-04.** Payloads in documentation examples, run when the agent reuses
  them. 1,070 skills, bypass 11.6-33.5%, while explicit instructions got 0% under
  strong defences; 2.5% evaded detection and alignment both [A]
  https://arxiv.org/abs/2604.03081. An example in a skill body is an attack surface
  that an "is this an instruction?" check does not address.
- **Semantic attacks, 2026-05.** Description-only framing got malicious variants
  chosen in 77.6% of paired trials; evasion avoided a blocking verdict in 36.5-100%
  of cases [A] https://arxiv.org/abs/2605.11418.

### Attacks on the learning loop itself

This is the part closest to the owner's idea. All are research prototypes; **none
tested Hermes or OpenClaw's Workshop, and no in-the-wild case was found.**

- **SkillJack, 2026-08-04.** On SkillX and Anything2Skill, with 150 trajectories.
  Safety detection on SkillX fell **from 98.5% on the poisoned trajectories to 11.4%
  on the extracted skills**: extraction launders the text. Attack success 56.2% and
  89.2%; 80% of attacks persisted after the source records were deleted [A]
  https://arxiv.org/abs/2608.03509.
- **PoisonedEvolution, 2026-08-06.** At 10% attacker share of evidence: 546/600 on
  SkillClaw, 369/600 on Trace2Skill, across six evolver models. Three consistent
  records of 30 were enough, and one was much weaker. "Evidence promotion is a
  security boundary" [A] https://arxiv.org/abs/2608.05563.
- **Trajectory Backdoor, 2026-08-08.** Queries only: repeated across tasks, they make
  the evolver write a trigger-dependent rule, with clean-task utility preserved [A]
  https://arxiv.org/abs/2608.08303. An attacker needs no skill-file access, only a
  channel into what the agent does.

### Persistence through memory

- **PMPA, 2026-09-12.** Instructions in benign outside sources are written to
  persistent memory. Injection and cross-session success: 73.7% / 55.5% on OpenClaw,
  66.9% / 81.7% on Claude Code. A prompt-level defence cut injection in many settings,
  but cross-session success "remains nearly unchanged" once written [V]
  https://arxiv.org/abs/2609.13889. So a defence at read time is weak, and the write
  is the place to stop it.
- **Earlier cases, second-hand:** SpAIware, 2024
(https://thehackernews.com/2024/09/chatgpt-macos-flaw-couldve-enabled-long.html); Rules File
Backdoor, 2025 (https://thehackernews.com/2025/03/new-rules-file-backdoor-attack-lets.html).

### What held and what was bypassed

| Defence | Result | Source |
|---|---|---|
| Keyword and pattern scan | Microsoft Cowork's static layer caught 10.7% of about 5,000 seeded injection variants, 0.4% after obfuscation. Vendor test; the behavioural layer was excluded | [V] https://promptarmor.com/resources/microsoft-scan-misses-1-million-malicious-skills |
| Signature services | VirusTotal 21.6% recall, Snyk Agent Scan 28.7%, with 4 and 82 false positives of 4,000 | [V] https://arxiv.org/abs/2606.07131 (MalSkillBench) |
| Model scanners, recall | Cisco LLM 92.7%; Sentry full 98.4% | [V] same |
| Model scanners, false alarms | Cisco LLM flags 1,465 of 4,000 benign; Sentry 937. Instruction-only attacks are weakest: Cisco LLM 32.1% on content manipulation | [V] same |
| Model scanner under direct attack | A framed injection flipped skillcop from CRITICAL to clean on its default model | [V] https://adversa.ai/blog/agent-skill-scanners-bypass-eight-tested |
| Eight open-source scanners | All bypassed: paraphrase, translation, homoglyphs, encoding, bundled scripts. Adaptive red team: 6.8% detection, 1.2% for the best pair. LLM stages excluded from that run, and the corpus has defects | [V] same |
| Reporting and auto-hide | Exists since the campaign; effect not measured [2] | The Hacker News above |
| API sandbox with no network | Stated as a limit, not attacked in any source found | [V] Anthropic overview |

Caution on MalSkillBench: its abstract says the strongest detector "collapses on
prompt-injection and agent-control attacks", while the results section read here gives
Sentry full 97.6% on prompt injection. These could not be reconciled from the parts
read. Sentry's full mode was not attacked adaptively in this benchmark, so high
recall on a fixed set says little about an attacker who sees the scanner. The
benchmark's authors state the root difficulty: maliciousness is "a semantic
relationship" between requested authority and advertised task, not an artifact.

**Reading the evidence.** Model scanners catch more than signatures, at a false alarm
rate that would be heavy per-skill for one owner. Every class of scanner was bypassed
when attacked adaptively. No study found measures a clean-context model checking one
short agent-written skill from a known source, which is a different and possibly easier
problem than sorting a marketplace; equally, SkillJack shows extraction removing the
signal such a check reads.

## 4. Acceptance patterns and their evidence

| Pattern | Used by | Evidence it catches real problems | Known limit |
|---|---|---|---|
| **Human review of text** | Hermes and OpenClaw (optional), Anthropic's checklist | No measured catch rate found. Unit 42 found by manual analysis what scanners passed, but those were professionals | Fatigue; owners approve what reads naturally (ADR-0034 said this). Self-learning frequency makes it worse. Invisible Unicode needs tooling to be seen |
| **Clean-context model review** | VirusTotal Code Insight, ClawScan (nature unconfirmed), ganglion's guard for memory | High recall on fixed sets, weaker adaptively and on instruction-only attacks (section 3) | Injectable; false alarms; the extraction step lowers its signal. Adversa: do not let a model suppress deterministic high findings |
| **Deterministic lint** | skills-ref, Hermes' guard, OpenClaw's critical findings | Good at format, size, credentials, symlinks, unicode. Weak on meaning: paraphrase beats it | Size limits themselves are evaded when oversize files are skipped rather than refused (`omnicogg`). Adversa: scanners read "the bytes in the file, not the bytes that execute" |
| **Trial runs in a sandbox** | Anthropic's checklist; a behavioural layer in Microsoft's (not evaluated) | MalSkillBench verified its samples at runtime, so running shows behaviour; no study found measured it as a gate | Trigger-conditional backdoors do not fire in a trial (Trajectory Backdoor) |
| **Evals with and without the skill** | `skill-creator`, Anthropic's enterprise guide | Evidence is for usefulness and triggering, **none for security**. TBA keeps clean-task utility, so a backdoored skill would pass | Costs model runs; the skill's author also writes the test unless separated |
| **Coexistence and description check** | Anthropic's guide; the 160-byte cap in OpenClaw | Guards against trigger stealing; 77.6% selection steering suggests it matters | Not measured as a defence |
| **Quarantine and staging** | OpenClaw proposals, Hermes `pending/`, ganglion's memory quarantine | Mechanism, not detection. PMPA supports stopping at the write | Only as good as the reviewer after it |
| **Provenance and taint at the source** | Called for by SkillJack, PoisonedEvolution, CSA; recorded partly by Hermes (`agent_created`) | Recommended, not measured in any product found. Addresses the root of the loop attacks | Laundering across sessions: 80% persistence after source deletion; an owner's pasted text may carry outside content without a mark |
| **Hash binding, pin, version control, rollback** | OpenClaw, Anthropic's guide, Letta's git | Stops silent drift, and enables recovery. No study measures it. `money-radar` shows content can still change at run time via fetches | Does not judge content; git history of a skill is undone only if someone notices |
| **Declared capabilities compared to behaviour** | Unit 42's BIV, Adversa's host-list diff advice | Found a large mismatch population, static and unconfirmed. 81% oversight implies many false alarms | Needs a declaration to compare; an agent's own draft declares what it likes |

## 5. What this means for ganglion

Facts that bear on the design, without choosing one:

- The owner's floor matches the evidence, but a taint rule alone may not suffice: TBA and
  SkillJack work through ordinary-looking input, and recall can carry earlier taint.
- Hermes' note applies in reverse: its gate is weak because `terminal()` is ungated. ganglion
  has no such shell yet, and M3d's host commands may change that.
- ADR-0034's question ("a fact or an instruction?") has no meaning for a skill, whose job is to
  instruct, so the guard's prompt cannot be reused as is.
- ADR-0042's revisit clause fires here: "a skill comes from anywhere but the owner's hand".

Open questions:

1. **Evidence:** may a draft draw only on sessions untainted throughout? How do recall and
   compaction carry taint? Is one session enough, given three records of 30 sufficed elsewhere?
2. **Model check:** what does it answer instead (does the authority fit the task; which hosts,
   tools and paths are named)? Is its verdict a gate or advice? Does it see the evidence too?
3. **Owner's view:** is a diff in Telegram enough, with unicode and encoded text made visible?
   What proposal rate keeps review real? Does an edit need the same acceptance as a new skill?
4. **Deterministic checks:** closed lists of hosts and tools, normalised unicode, refusal of
   oversize, no fetch-at-run? What about code examples in the body (DDIPE)?
5. **Rights:** may an agent-written skill set `model` or `privacy`, or only narrow rights? Does it
   rank below owner skills? Can scheduled tasks use it, and is an edit a change of grant (R-POL-8)?
6. **Model use:** the project aims at the least model power. Can a deterministic signal (repeated
   tool sequence, a correction, an explicit ask) replace a background review? Do evals add
   anything for security?
7. **Is the loop needed first,** or does "the owner asks, the agent drafts" suffice? It avoids the
   attacks that need unattended extraction, at a cost in convenience.
8. **Lifecycle and format:** where does provenance live, as frontmatter is author-writable? How
   does the nightly audit see these skills? Adopt the spec's names and `metadata`? Does importing
   others' skills, a separate and more dangerous door, stay closed?

## Not verified

- Koi Security's original post (the URL redirects); 1,184 ClawHavoc skills; the CSA note's
  "26,000 agents" incident, not followed up.
- Whether ClawScan is model-based; the methods of Anthropic's and Microsoft's scans; OpenClaw's
  scanner rules (docs only, not source).
- Full text of [A] papers; for PMPA and MalSkillBench, only the abstract and cited excerpts.
- Letta's automatic learning; Goose's creation path; Cursor's memory feature.
- Hermes "no taint check" covers the two files named, not the repository.
- No in-the-wild attack on an agent's own learning loop was found: weak evidence of absence.
