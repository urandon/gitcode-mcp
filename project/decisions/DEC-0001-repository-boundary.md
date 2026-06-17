# DEC-0001 - Repository Boundary

Status: accepted
Date: 2026-06-17

## Context

GitCode tracker/wiki migration and synchronization need reusable tooling for projection, local cache, link rewriting, and MCP access. Keeping that implementation inside any project-specific control repository would mix reusable code with project-local decisions and handoffs.

## Decision

Implementation work for GitCode/local-cache/MCP tooling lives in this repository.

Project-specific control surfaces keep their own plans, decisions, and accepted summaries.

## Consequences

- This repo owns implementation issues, handoffs, tests, fixtures, and code.
- Migration and cutover decisions that affect a specific team should stay in that team's own control surface.
- The tooling can be published without dragging non-public source history or project-specific control surfaces with it.
