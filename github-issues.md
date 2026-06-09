# GitHub Issue Conventions — Convergent Routing Analyzer

This file is the **single source of truth for how issues are written** in this repo. Every issue must
conform to the format below. Keep it updated as conventions evolve. (Planning context lives in the local,
untracked `project-spec.md`; section references like `§R1` point at its *Revision 2* decisions.)

---

## Issue format (copy this template)

```markdown
## Summary
<one sentence: what this issue delivers>

## Phase
Phase <N> — <name>

## Context
<why this matters; link to the spec section, e.g. "project-spec.md §R1">

## Tasks
- [ ] <concrete, checkable step>
- [ ] <concrete, checkable step>

## Deliverable
<the runnable/observable artifact that proves this is done>

## Acceptance Criteria
- [ ] <objective, testable condition>
- [ ] <objective, testable condition>

## Dependencies
- Blocked by: #<id> (or "none")
- Blocks: #<id> (or "none")

## Owner / Area
<area>  (one of: infra, data-pipeline, database, routing-engine, visualization, docs)
```

### Rules
1. **Title**: `[Phase N] <imperative summary>` — e.g. `[Phase 0] Scaffold repo structure & Go module`.
2. **One deliverable per issue.** If an issue has two unrelated "Deliverable" lines, split it.
3. **Tasks and Acceptance Criteria are checkboxes** so progress is visible on the board.
4. **Every issue carries**: a `phase-*` label + exactly one area label (`infra`, `data-pipeline`,
   `database`, `routing-engine`, `visualization`) + optional type labels (`contract`, `scaffolding`,
   `documentation`).
5. **Frozen contracts** (`§R0`) get the `contract` label and must link their golden-fixture task.
6. **Dependencies are explicit.** A blocked issue names its blocker by number.

---

## Phase 0 — Foundations (current backlog)

All Phase 0 issues are blocked by **#1** (scaffolding) unless noted. The three `contract` issues (#3–#5)
are the board's frozen-at-kickoff contracts (`§R0`) and gate every later phase.

| # | Title | Area | Labels |
|---|---|---|---|
| 1 | [Phase 0] Scaffold repo structure & Go module | infra | `phase-0` `scaffolding` `infra` |
| 2 | [Phase 0] Define core ports (Graph, CongestionProvider, CostFunction, Router) | routing-engine | `phase-0` `routing-engine` |
| 3 | [Phase 0] Freeze contract: canonical `segment_id` (§R1) | database | `phase-0` `contract` `database` |
| 4 | [Phase 0] Freeze contract: `edge_attributes` export schema (§R2) | database | `phase-0` `contract` `database` |
| 5 | [Phase 0] Freeze contract: `segment-congestion` schema v2 (§R4) | data-pipeline | `phase-0` `contract` `data-pipeline` |
| 6 | [Phase 0] docker-compose profiles + healthchecks (§R7) | infra | `phase-0` `infra` |
| 7 | [Phase 0] Makefile targets + CI matrix (§R7) | infra | `phase-0` `infra` |
