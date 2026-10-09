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

 ## Setup and Deploy
 ### Build
 ```
# Indexer
cd ~/code/ai-factory/design-indexer
go mod tidy
go build -o ~/.local/bin/design-indexer ./cmd/design-indexer

# MCP server
cd ~/code/ai-factory/design-mcp
go mod tidy
go build -o ~/.local/bin/design-mcp ./cmd/design-mcp
```
### Create the Qdrant Collection
Done automatically on first indexer run, but you can pre-create for custom config:
```
curl -X PUT http://localhost:6333/collections/design_docs \
  -H 'Content-Type: application/json' \
  -d '{"vectors": {"size": 1024, "distance": "Cosine"}}'
```
### First Index Run
```
~/.local/bin/design-indexer -config ~/code/ai-factory/design-indexer/config.yaml
# Logs:
# INFO index complete docs=47 chunks=312 skipped=0 duration=14.2s
```
Re-runs are fast because unchanged docs are skipped via `doc_hash`.

### Start the MCP server
```
~/.local/bin/design-mcp -config ~/code/ai-factory/design-mcp/config.yaml
# INFO design-mcp starting listen=:8767 collection=design_docs
Add a launchd plist for persistent operation, same pattern as telegram-mcp.
```
### Smoke Test
```
npx @modelcontextprotocol/inspector --transport sse http://localhost:8767/sse
```
Try:
 - `find_decision(topic="messaging system")` → should return your Kafka ADR.
 - `list_adrs(status="Accepted")` → full inventory.
 - `search_docs(query="deployment runbook")` → runbooks surface.

### Wire Into Agents
Add to opencode `~/.config/opencode/opencode.json`
```
{
  "mcp": {
    "design": {
      "type": "remote",
      "url": "http://localhost:8767/sse",
      "enabled": true
    }
  }
}
```
Cursor `~/.cursor/mcp.json`:
```
{
  "mcpServers": {
    "design": { "url": "http://localhost:8767/sse" }
  }
}
```
Claude Code:
```
claude mcp add --transport sse design http://localhost:8767/sse
```
### Agent Nudge
Add to your `AGENTS.md` / `CLAUDE.md` / Cursor rules:
```
## Design Decisions

Before proposing changes to messaging, storage, auth, deployment,
observability, or any cross-cutting concern:

1. Call `find_decision(topic=...)` to check the authoritative ADR.
2. If the ADR status is Superseded or Deprecated, call `list_adrs(status=Accepted)`
   to find the current decision.
3. When summarising a decision in your response, call `cite_source(claim=...)`
   and include the citation inline.
4. If no ADR covers the topic and the change is architectural, suggest the
   user author a new ADR before implementing.
```
### ADR Workflow (Optional Enhancement)
To close the loop, add a tiny helper that creates new ADRs with the right frontmatter. Not required for the MCP to work, but it keeps your corpus clean.
Create `~/code/ai-factory/scripts/new-adr.sh`:
```
#!/usr/bin/env bash
set -euo pipefail
ROOT="${ADR_ROOT:-$HOME/code/design-docs/adr}"
mkdir -p "$ROOT"

last=$(ls "$ROOT" 2>/dev/null | grep -E '^[0-9]{4}-' | sort | tail -1 | cut -c1-4 || echo 0000)
next=$(printf "%04d" $((10#$last + 1)))
title="${*:-untitled}"
slug=$(echo "$title" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9]/-/g' | sed 's/-\{2,\}/-/g' | sed 's/^-\|-$//g')
file="$ROOT/$next-$slug.md"

cat > "$file" <<EOF
---
id: $next
title: $title
status: Proposed
date: $(date -u +%Y-%m-%d)
deciders: [$USER]
tags: []
---

# ADR $next: $title

## Context

## Decision

## Consequences

### Positive

### Negative

## Alternatives considered
EOF

echo "created $file"
```
```
chmod +x ~/code/ai-factory/scripts/new-adr.sh
new-adr.sh "Use NATS JetStream for inter-service events"
```
## Interaction Pattern With the Rest of the Stack
Here's what happens when you tell an agent in Cursor:
> "Add Redis caching to the user profile endpoint."
A well-configured agent now does roughly this:
 1. `find_decision(topic="caching strategy")` → maybe surfaces ADR 0017 "Use Redis for ephemeral caching, 15min TTL default".
 2. `search_code(query="user profile endpoint")` → locates the handler via mcp-code.
 3. `search_adrs(question="user profile data policy")` → confirms no PII rules are violated.
 4. Writes the change via cache-proxy (confidence-gated — may stay local).
 5. `cite_source(claim="Redis is our caching layer per ADR 0017")` → gets a citation.
 6. Runs `pipeline_run(service="myservice", command="full")`.
 7. On release: Telegram approval with the citation included in the context.
Every architectural claim is backed by a stored decision. You cannot accidentally violate your own standards; your agent won't let you.
