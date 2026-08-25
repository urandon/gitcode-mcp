# Admin UI design system

The embedded admin UI is one operator console, not a collection of command-specific screens. Every top-level view and repository tab must use the same theme tokens, typography, icon roles, surface hierarchy, and interaction states. The implementation source of truth is `web/src/routes/+page.css`; this document records the durable product rules that visual review and future UI work must preserve.

## Visual direction

The console is quiet, information-dense, and operational. It uses a warm neutral canvas, restrained teal accents, thin boundaries, low-radius surfaces, and generous page-level whitespace. Density belongs inside structured cards and tables, but must not reduce legibility or make one tab appear to belong to a different product.

All views share these invariants:

- the sidebar, page width, page title, introductory copy, and section-heading rhythm do not change when switching views;
- cards use the same surface, boundary, radius, and shadow tokens;
- status always has a textual label and never depends on color alone;
- safe observation is visually quieter than an explicit mutation; destructive or provider-boundary actions require a confirmation state;
- no raw cache path, credential, token, source body, or provider endpoint is used as decorative UI content.

## Theme contract

The visible selector has exactly `Light`, `Dark`, and `System`. `System` is the default and follows `prefers-color-scheme`; an explicit choice is stored locally. All component colors must resolve through the semantic CSS tokens below rather than view-specific literals:

- `--page` and `--sidebar` define the application canvas;
- `--surface`, `--line`, and `--line-soft` define cards and grouping;
- `--text`, `--muted`, and `--subtle` define the content hierarchy;
- `--accent`, `--accent-soft`, and `--accent-dark` define selection, healthy state, and safe primary action;
- `--warning*` and `--danger*` are reserved for typed operational state.

New components must be checked in Light, Dark, and System. A selected tab, disabled control, focus ring, status, table header, dialog backdrop, and empty state must remain distinguishable in both effective color schemes.

## Typography

The UI uses the system sans stack headed by Inter when available and the system monospace stack for opaque ids, scores, timestamps, and commands. The shared scale is:

| Role | Token or size | Use |
| --- | --- | --- |
| Page title | `clamp(40px, 4vw, 54px)` | One `h1` per view or selected entity. |
| Section title | `22px` | Major content cohort within a view. |
| Panel title | `20px` | Workbench, job-state, and confirmation panels. |
| Card title | `--type-card-title` (`14px`) | Cache, result, diagnostic, and compact entity titles. |
| Body/control | `--type-body` / `--type-control` (`13px`) | Primary reading text and all form controls. |
| Caption | `--type-caption` (`12px`) | Supporting explanation and result snippets. |
| Micro label | `--type-micro` (`11px`) | Uppercase labels, ids, score labels, and table headings. |

Visible text must not be smaller than 11px. Weight, case, letter spacing, color, and monospace treatment create hierarchy below body size; shrinking individual tabs to fit more content does not.

## Icons

Lucide is the only interface icon set. Do not add emoji, hand-drawn SVG, CSS icons, or a second icon library. Use icons by semantic role, not as decoration:

- navigation icons use 18px source icons and a consistent 1.8 stroke width;
- inline action icons use 15–16px source icons;
- compact entity and coverage icons use a 32px `--icon-frame-medium` accent-soft frame;
- effect-ledger icons use a 28px `--icon-frame-small` frame;
- prominent readiness icons use a 44px `--icon-frame-large` frame;
- status uses the shared dot-and-label component rather than a competing status icon.

An icon's meaning must remain stable across tabs. For example, database denotes a cache, activity denotes a job, wrench denotes maintenance, heart pulse denotes diagnostics or a health probe, and search denotes retrieval.

## Components and semantic cohorts

### Global navigation

The brand, five top-level destinations, and theme selector are persistent. The selected destination changes color, weight, and surface together. Responsive icon-only navigation retains accessible names and tooltips.

### Observation

Overview rows, cache topology, coverage lanes, result comparisons, job history, and diagnostics are observational surfaces. They use neutral surfaces and accent color only for current healthy state, selection, links, or exact-match evidence.

### Controls

Filters, text inputs, selects, and ordinary buttons use the shared 40px control height and 13px control type. Labels use the 11px micro role. A workbench explains its boundary before controls, produces a plan before mutation, and keeps the effect ledger distinct from the action button.

### Status and feedback

Loading, empty, partial, degraded, stale, recovered, interrupted, waiting, success, and failure are explicit textual states. Receipts remain visible after completion. Changing any input that contributes to a plan invalidates the old plan and receipt before another apply can be attempted.

### Dialogs

Mutation dialogs repeat the target, cache reference, exact plan, and data boundary. Initial focus lands on the safe action, focus is trapped while open, Escape cancels, and focus returns to the invoking control.

## Layout and responsiveness

The desktop shell uses a persistent sidebar and a bounded main column. Section spacing can be generous; content inside cards may be compact but must follow the shared type scale. At narrower widths, grids collapse before text is reduced. Tables may scroll horizontally. At the mobile breakpoint the sidebar becomes icon-only, forms become one column, and status labels remain present in content.

The minimum supported viewport is 320 CSS pixels. No page may create document-level horizontal overflow at 390 CSS pixels.

## Review checklist

Before committing an admin UI change:

1. compare every affected screen with an adjacent top-level view at the same viewport and zoom;
2. exercise Light, Dark, and System, including a system preference change;
3. verify page title, section heading, card title, body, caption, micro label, control height, icon frame, radius, and surface tokens;
4. verify keyboard focus, dialog focus return, status text, empty/error states, and reduced-width overflow;
5. run frontend unit and Playwright tests, rebuild embedded assets, and run `scripts/check-admin-ui-assets.sh` as part of the release gate;
6. record visual QA evidence in the active issue or pull request without committing local browser artifacts.

The broader runtime boundary, API surface, and release procedure remain documented in [Embedded admin UI](admin-ui.md) and [Admin UI release gates](admin-ui-release-gates.md).
