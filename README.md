 # Design/ADR MCP Server
A dedicated MCP server for architectural knowledge: ADRs, design docs, runbooks, RFCs, and READMEs. Agents consult this before making architectural decisions, which keeps them aligned with past choices and dramatically improves the quality of their proposals.

 ## Why This Deserves Its Own Server
You might ask: "The code-search MCP already has a search_docs tool. Why a second server?"
Three reasons:
 1. Different retrieval semantics. Code chunks are small, dense, and queried by function-level questions. Docs are long-form, hierarchical, and queried by concept-level questions. The chunking, embedding, and ranking should be different.
 2. Different write paths. Code is indexed from a watcher on your repo. Docs come from multiple sources: in-repo docs/ folders, a central ~/code/design-docs/ collection, maybe pulled from Confluence/Notion/Google Docs later. Separate pipelines.
 3. Different agent affordances. For code, agents want snippets. For design docs, agents want decisions — "Should we use Kafka or NATS?" has an answer somewhere in your ADRs, and the agent needs to surface that specific decision, not just related text.

The design-MCP has tools tuned to how architectural knowledge is actually used:
 - search_adrs(question) → "What did we decide about X?"
 - search_docs(query) → broader doc search
 - find_decision(topic) → specifically returns the decision + status + consequences
 - list_adrs(status?) → "What's accepted vs. deprecated?"
 - get_adr(id) → full retrieval by number
 - cite_source(fragment) → agents attribute claims

 ## Architecture
```
┌──────────────────────────────────────────────────────────────┐
│  Multiple doc sources                                        │
│   • ~/code/design-docs/        (central ADR repo)            │
│   • ~/code/services/*/docs/     (per-service runbooks)       │
│   • ~/code/services/*/README.md                              │
│   • ~/code/services/*/ADR/      (per-service ADRs)           │
└──────────────────────────────────────────────────────────────┘
                            │
                            ▼
┌──────────────────────────────────────────────────────────────┐
│  design-indexer (daemon)                                     │
│   • Walks configured source roots                            │
│   • Parses frontmatter + Markdown structure                  │
│   • Detects ADRs by filename pattern (NNNN-*.md)             │
│   • Section-aware chunking (H2/H3 boundaries, respects       │
│     code fences)                                             │
│   • Enriches with doc title, section path, status, tags      │
│   • Upserts to Qdrant: design_docs collection                │
└──────────────────────────────────────────────────────────────┘
                            │
                            ▼
┌──────────────────────────────────────────────────────────────┐
│  design-mcp (daemon, SSE on :8767)                           │
│   • search_adrs / find_decision / list_adrs / get_adr        │
│   • search_docs / get_doc / cite_source                      │
│   • Query cache (LRU on query embeddings)                    │
└──────────────────────────────────────────────────────────────┘
```
Shared with the rest of your stack: Qdrant, Ollama for embeddings. Nothing new to install.

 # ADR Format Convention
Design-MCP works best when ADRs follow a predictable format. Not strict — just enriched if present. Here's the convention it understands:
```
---
id: 0023
title: Use Kafka for inter-service events
status: Accepted         # Proposed | Accepted | Deprecated | Superseded
date: 2024-11-12
deciders: [slaghuis]
supersedes: 0014
superseded_by:
tags: [messaging, infrastructure]
---

# ADR 0023: Use Kafka for inter-service events

## Context

We need an asynchronous event bus...

## Decision

Adopt Apache Kafka 3.x with the following constraints...

## Consequences

### Positive
- Decoupled services
- Replay capability

### Negative
- Operational complexity
- Requires at least 3 brokers

## Alternatives considered

- NATS JetStream — rejected because...
- RabbitMQ — rejected because...
```
The indexer extracts frontmatter if present, falls back to filename and first H1 if not. ADRs without frontmatter still work; they just lose the status/supersedes awareness.


