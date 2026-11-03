# Agent security baseline: what the sources say

- **Date:** 2026-11-02
- **Task:** M3b task 1 ([plan](../plans/p1-m3b-agent-world.md)); design
  section 4 ([design](../plans/p1-m3b-m3c-design.md#4-security-baseline-both-milestones))
- **Question:** what do the known analyses of attacks on tool-using
  agents say, and what do they ask of ganglion's baseline, now that M3b
  gives the agent tools, memory, a scheduler and the web?
- **Status:** every source below was opened on 2026-11-02. The OpenClaw
  docs were read from the local install (version 2026.10.3) and the
  online copy of one page was checked against it. The decision is
  [ADR-0031](../adr/0031-agent-security-baseline.md).

## The threat in one paragraph

From M3b the agent holds all three things an attacker needs: private
data (memory, history, the owner's chats), untrusted content (web
pages, search results, forwarded messages) and channels out
(`message.send`, `web.fetch` to any host, scheduled tasks that run
unattended). Any text the model reads can carry instructions. A model
cannot reliably tell data from instructions, so the defence has to sit
in code the model does not control.

## Sources

### OWASP Top 10 for LLM Applications (2025)

- **Where:** <https://genai.owasp.org/llm-top-10/>, and the pages of
  each entry linked below. Checked 2026-11-02.
- **What:** the 2025 list is LLM01 Prompt Injection, LLM02 Sensitive
  Information Disclosure, LLM03 Supply Chain, LLM04 Data and Model
  Poisoning, LLM05 Improper Output Handling, LLM06 Excessive Agency,
  LLM07 System Prompt Leakage, LLM08 Vector and Embedding Weaknesses,
  LLM09 Misinformation, LLM10 Unbounded Consumption.

Four entries bear on M3b:

- **[LLM01 Prompt Injection](https://genai.owasp.org/llmrisk/llm01-prompt-injection/).**
  Separates direct injection (the user's own input) from indirect
  injection, where the model takes in outside content such as web pages
  or files. Mitigations named: constrain the model's behaviour in the
  system prompt; segregate and clearly mark external content; least
  privilege, with tokens and function handling kept in application code;
  human approval for high-risk actions. The page says no method is
  known to prevent prompt injection entirely.
- **[LLM02 Sensitive Information Disclosure](https://genai.owasp.org/llmrisk/llm022025-sensitive-information-disclosure/).**
  Data, credentials included, leaking through the model's output.
  Mitigations: sanitise and redact, least-privilege access to sensitive
  data, no sensitive detail in error messages or configuration. It notes
  that restrictions written into the system prompt "may not always be
  honored and could be bypassed via prompt injection".
- **[LLM04 Data and Model Poisoning](https://genai.owasp.org/llmrisk/llm042025-data-and-model-poisoning/).**
  Its scope is pre-training, fine-tuning and embedding data. **It does
  not cover poisoning of an agent's runtime memory explicitly**; we apply
  it by analogy, because a memory entry the model reads every turn acts
  like training data that never expires. Mitigations that carry over:
  track provenance, keep data under version control so a change can be
  found and reverted, vet sources, sandbox unverified sources.
- **[LLM06 Excessive Agency](https://genai.owasp.org/llmrisk/llm062025-excessive-agency/).**
  Damaging actions done "in response to unexpected, ambiguous or
  manipulated outputs". Root causes: excessive functionality, excessive
  permissions, excessive autonomy. Mitigations: human-in-the-loop
  approval of high-impact actions, minimal permissions and functions,
  and complete mediation: authorisation enforced in the system that
  runs the action, not decided by the model.

### Simon Willison: the lethal trifecta

- **Where:** "The lethal trifecta for AI agents: private data, untrusted
  content, and external communication", 16 June 2025,
  <https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/>. Checked
  2026-11-02.
- **What:** "If your agent combines these three features, an attacker
  can easily trick it into accessing your private data and sending it to
  that attacker." The three legs are access to private data, exposure to
  untrusted content, and the ability to communicate externally. The
  advice is to avoid the combination: break one leg.

### Simon Willison: the dual LLM pattern

- **Where:** "The Dual LLM pattern for building AI assistants that can
  resist prompt injection", 25 April 2023,
  <https://simonwillison.net/2023/Apr/25/dual-llm-pattern/>. Checked
  2026-11-02.
- **What:** a privileged LLM with tools sees only trusted input; a
  quarantined LLM with no tools handles untrusted content; a controller,
  ordinary code, runs actions and passes untrusted text around by
  reference (`$VAR1`) so the privileged LLM never reads it. Output of the
  quarantined LLM may reach the privileged one only when it can be
  verified, for example a choice from a fixed set of categories.
- **Caveats, by the author:** it adds complexity and hurts the user
  experience; it is open to social engineering; one leak of untrusted
  text into the privileged LLM undoes it. A 2025 update on the page
  points to Google DeepMind's CaMeL paper as a more evolved design.
- **CaMeL:** "Defeating Prompt Injections by Design" (Debenedetti et
  al.), <https://arxiv.org/abs/2503.18813>. Checked 2026-11-02. A
  protective layer around the model keeps untrusted data from changing
  the program's control flow and uses capabilities to stop
  exfiltration; on AgentDojo it solves 77% of tasks with provable
  security against 84% undefended.

### Anthropic: the `web_fetch` URL rule

- **Where:** the web fetch tool page,
  <https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-fetch-tool>
  (the old `docs.anthropic.com` address redirects there). Checked
  2026-11-02.
- **What:** "To minimize exfiltration risks, Claude cannot fetch URLs
  that appear only in its own output." Allowed sources are URLs in user
  messages, in client-side tool results, and from earlier web search or
  web fetch results; not URLs that appear only in the model's own output
  or only in the system prompt. A URL that seems to carry a credential is
  refused unless that credential is in the system prompt or a user
  message. Further limits: `max_uses`, `allowed_domains` /
  `blocked_domains`, `url_sources` to drop a source, and private
  addresses are refused. The page still warns of residual risk when
  untrusted input and sensitive data meet.

### OpenClaw: external-content wrapping and the MCP loopback bridge

OpenClaw is the closest neighbour (spec 1.1). Its docs were read from
the local install, version 2026.10.3:

- **Prompt injection** (`docs/gateway/security/prompt-injection.md`):
  - any untrusted content the bot reads is a threat surface, even when
    only the owner can message the bot;
  - external content is wrapped in `<<<EXTERNAL_UNTRUSTED_CONTENT …>>>`
    markers with `Source: External` metadata;
  - chat-template special tokens (`<|im_start|>` and the like) are
    stripped from wrapped content, so a page cannot forge a role
    boundary on self-hosted models;
  - it recommends a read-only or tool-less "reader agent" that
    summarises untrusted content for the main agent;
  - smaller models are easier to steer, so tool-using agents should use
    strong ones;
  - bypass flags (`allowUnsafeExternalContent`) exist and are to be kept
    off.
- **`web_fetch`** (`docs/tools/web-fetch.md`): blocks private and
  internal hostnames and re-checks redirects (at most 3 by default);
  extracts the main content with Readability; caps output at 20,000
  characters by default; returns `externalContent` metadata with the
  wrapped text.
- **`web_search`** (`docs/tools/web.md`): results carry
  `externalContent: {untrusted: true, wrapped: true}`; provider text is
  stripped of any existing envelope and re-wrapped once at the core
  boundary, so a provider cannot spoof the marker. Key-free options are
  DuckDuckGo, an "experimental, unofficial" HTML scrape that may break
  (`docs/tools/duckduckgo-search.md`), and self-hosted SearXNG.
- **MCP loopback bridge** (`docs/gateway/cli-backends.md`, "Bundle MCP
  overlays"; the online copy at
  <https://docs.openclaw.ai/gateway/cli-backends> says the same, checked
  2026-11-02):
  - a CLI backend such as `claude-cli` gets Gateway tools from a
    loopback HTTP MCP server, authenticated with a per-run grant
    (`OPENCLAW_MCP_TOKEN`) active only for the current attempt;
  - tool access is bound to the Gateway's session, account and channel
    context, not to headers from the child process;
  - the CLI gets a generated strict MCP config file, even when no MCP
    servers are enabled, so background runs stay isolated.

  **Not confirmed in the OpenClaw docs:** the docs say "strict MCP
  config file", not the `--strict-mcp-config` flag by name. The flag
  itself is in the Claude Code CLI reference
  (<https://code.claude.com/docs/en/cli-reference>, checked 2026-11-02):
  "Only use MCP servers from `--mcp-config`, ignoring all other MCP
  configurations". The same page documents `--tools ""` as turning off
  all built-in tools.

## What this asks of ganglion

| Finding | Source | Rule in the baseline (ADR-0031) |
|---|---|---|
| Untrusted content steers the model; no prompt fixes it | OWASP LLM01; OpenClaw | Rule 1: a tainted session; rule 6: least privilege; layer 1 of directives in code |
| Injected text stays in the history and steers later turns | Lethal trifecta; dual LLM ("one leak undoes it") | Rule 1: taint per session, not per turn; rule 4: compaction keeps the marks |
| Break one leg of the trifecta | Lethal trifecta | Rules 1 and 2: in a tainted session every channel out needs a press |
| A URL the model builds is a channel out | Anthropic `web_fetch` | Rule 2: `web.fetch` without a press only on URLs from the owner or from search results |
| Mark external content so a provider cannot spoof the mark | OpenClaw `web_search`, prompt injection | R-POL-4, R-WEB-1: the core adds the mark once, at its boundary |
| A model with no tools and no tainted context can judge untrusted text | Dual LLM; OpenClaw's reader agent | Rule 3: the clean-context check, which answers a fixed question (fact or instruction?) |
| Long-lived data needs provenance and version control | OWASP LLM04 (by analogy) | Rule 3 and R-MEM-5: source recorded, every write a git commit |
| Human approval for high-impact actions, minimal permissions, mediation in code | OWASP LLM06 | Rules 1, 6 and 9; R-POL-1 |
| Unattended runs need bounded rights | OWASP LLM06; OpenClaw (automations capped by a finite tool list) | Rule 6 and R-POL-8: a grant approved once at task creation |
| Credentials leak through output and logs | OWASP LLM02 | Rule 5: secrets never reach the model, redacted before journals and logs |
| The tools of a CLI run are bound to the caller's context, not the child's | OpenClaw MCP bridge | R-PRV-6: session, principal and turn from the core's context |
| Every decision can be audited | OWASP LLM06; R-POL-3 | Rule 7: everything is journalled |

## What we do not take

- **The full dual LLM or CaMeL design.** Passing untrusted text by
  reference needs its own interpreter and changes how every tool
  returns data. Too much for M3b; the clean-context check takes the
  part that fits: a model with no tools judging one question.
- **Model choice as a defence.** OpenClaw's advice to use strong models
  for tool-using agents is sound, but ganglion runs a cheap model first
  ([ADR-0022](../adr/0022-haiku-first-during-development.md)). The rules
  in code do not depend on the model resisting.
- **Disabling the web for tool-using agents.** OpenClaw suggests it; the
  owner's acceptance test ("send me the rates") needs the web, so taint
  and the URL rule bound it instead.
