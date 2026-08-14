# Lessons

## Don't let parallel background agents write to a shared log file

**What happened:** During the reactor/io_uring rewrite of `v2/`, several
`general-purpose` subagents ran concurrently, each apparently following the
inherited project `CLAUDE.md` convention ("Document Results: add a review
section to `tasks/todo.md`") on its own initiative. Each one overwrote
`tasks/todo.md` with only its own narrow sub-task's notes, clobbering
whatever the previous agent (or the original scaffold-phase log) had written.
The original 8-module scaffold summary was lost from disk entirely — it only
survived because it happened to still be present earlier in the orchestrating
session's own transcript, and could be manually reconstructed from there.

**Rule for next time:** When dispatching multiple agents in parallel that
might each try to write to a shared, singleton file (`tasks/todo.md`,
`tasks/lessons.md`, a shared CHANGELOG, etc.), explicitly tell each agent
**not** to touch that file — reserve it for the orchestrator to write once,
after collecting every agent's final report. If per-task notes are wanted
mid-flight, have each agent write to its own scoped file (or just return the
notes in its final report text) instead of a shared path.
