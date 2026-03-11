# Documentation – Guidelines for AI assistants

This directory contains all Flight Control documentation. Use this file to place new content correctly and follow project conventions.

## Structure

- **docs/user/** – User-facing documentation: installation, configuration, using the CLI, managing devices and fleets, references. Entry point: [docs/user/README.md](user/README.md).
- **docs/developer/** – Developer-facing: building, running, architecture, enhancements. Entry point: [docs/developer/README.md](developer/README.md).
- **docs/user/installing/** – Service, CLI, and agent installation; auth, database, imagebuilder, and other configuration.
- **docs/user/using/** – Day-to-day usage: CLI, provisioning, managing devices/fleets, image builds, observability, use cases.
- **docs/user/references/** – API resources, CLI commands, events, metrics, security, compatibility.
- **docs/developer/architecture/** – System and component architecture, versioning, observability, alerts.
- **docs/developer/enhancements/** – FEPs (Flight Control enhancement proposals).
- **docs/images/** – Diagrams (SVG). Excalidraw-origin diagrams must be exported with "Embed Scene" (see lint-diagrams below).

## Where to add or edit content

- **User procedures (install, configure, use):** Under `docs/user/` in the appropriate subsection (e.g. `installing/`, `using/`, `building/`).
- **References (API, CLI, concepts):** `docs/user/references/` or linked from [docs/user/README.md](user/README.md).
- **Developer setup, architecture, design:** `docs/developer/` or `docs/developer/architecture/`.
- **New feature or design proposal:** Consider a FEP under `docs/developer/enhancements/`.

Keep the existing split: user docs for operators and end users, developer docs for contributors and internal design.

## Format and style

- **Markdown** for all docs. User docs are linted with markdownlint and spellcheck (see below).
- **Links:** Prefer relative links between docs (e.g. `[Installing the agent](installing/installing-agent.md)`).
- **Diagrams:** Mermaid in `.md` is fine. For Excalidraw, export SVG with "Embed Scene" enabled so `make lint-diagrams` passes.

## Documentation style

Documentation should be clear, consistent, appropriate for global audiences, and easy to translate. The following conventions align with common technical-writing standards.

### Tone and voice

- **Direct and clear:** Use second person ("you") where appropriate; prefer active voice and present tense. Be succinct; avoid wordy or redundant phrases.
- **Audience-appropriate:** Use a less conversational tone for technical and API documentation; a more conversational tone is acceptable for getting-started or tutorial content. Avoid slang, idioms, sarcasm, and humor that does not travel across cultures.
- **Global audience:** Keep sentences short and simple (aim for roughly 32 words or fewer). Avoid colloquialisms and metaphors; use plain, direct language. Write so that content can be translated reliably.

### Language and grammar

- **Contractions:** In product and reference documentation, avoid contractions to reduce ambiguity and simplify translation. Contractions may be used in quick-start or clearly informal content if used consistently.
- **Inclusive language:** Prefer **blocklist** / **allowlist** (or **denylist** / **allowlist**) instead of blacklist/whitelist. Prefer **primary** / **secondary** (or **source** / **replica**, **controller** / **worker**, etc.) instead of master/slave. For people or person-based "user," use "who"; for system or inanimate "user" (e.g. a Linux user account), use "that" only when necessary and when you cannot rewrite around it.

### Minimalism and findability

- **Reader-focused:** Focus on what the reader needs to do and why. Separate conceptual or background information from step-by-step tasks. Avoid long introductions and unnecessary context.
- **Titles and headings:** Use **sentence-style capitalization** for titles and headings. Keep headings between about 3 and 11 words so they are clear and findable. Do not end headings with a period unless they contain more than one sentence. Avoid question-style headings in reference and concept topics; questions are acceptable in how-to or tutorial headings when used sparingly.
- **Short descriptions:** Every major topic or assembly should have a short description (abstract) that states user intent and why the task matters. Place it before any admonitions. Make content scannable with short paragraphs and lists where appropriate.

### Lists

- Use **vertical lists** for series or parallel items instead of long in-sentence enumerations. Capitalize the first word of each list item.
- Use a **complete sentence** to introduce a list when possible; end the lead-in with a colon if the list follows immediately.
- Use **numbered lists** for procedures or when order matters; use **bulleted lists** when order does not matter. Avoid nesting lists beyond two levels when possible; never exceed three levels.
- Keep list items **parallel** in structure (all fragments or all complete sentences).

### Procedures

- Use **numbered steps**; one main action per step. Break long procedures into smaller tasks with a brief summary or list of sub-procedures at the start.
- Put **prerequisites** in a separate section (e.g. "Prerequisites" or "Before you begin"). Write them as checks that must be true or completed before starting; use parallel structure.
- Introduce the procedure with a heading (e.g. "Procedure") or a lead-in sentence ending with a colon. Use **imperative** phrasing in steps (e.g. "Open the file" not "You can open the file" or "The user opens the file").

### Code and command examples

- Use **one command per code block** per procedure step when possible. Put **command input** and **example output** in **separate code blocks** for clarity and copy-paste.
- Use a **monospace** font for code and commands. Introduce examples with a **complete sentence**; use a colon if the example follows immediately. Do not split a sentence so that part of it appears before the example and part after.
- **User-replaced values (placeholders):** Use angle brackets and descriptive names, e.g. `<node_name>`, `<path>`. Use lowercase and underscores for multi-word placeholders. In code blocks, keep placeholders in monospace; explain them after the block (e.g. with a definition list or short sentences) when needed.

### Admonitions (notes, warnings, tips)

- Use admonitions sparingly; avoid stacking several in a row. Keep each admonition **short**; do not put full procedures inside an admonition.
- Reserve **Note** for helpful extra guidance; **Important** for information the user must not skip; **Warning** for risk of damage, data loss, or serious issues (and include cause and mitigation); **Tip** for optional shortcuts or alternatives.

### Links

- **Link text** must describe what the user will find at the target (e.g. "Installing the agent" not "click here"). Use a concise phrase or sentence fragment so readers can decide whether to follow the link.

### Accessibility

- Do **not** rely on **color alone** to convey information; provide the same information in text or structure. Avoid instructions that depend only on direction (left, right, above, below) when a screen reader or non-visual user cannot infer them.
- Provide **meaningful alternative text** for every image, diagram, or icon that conveys information (describe function or content, not appearance). Do not use images of text where actual text can be used.
- Use **descriptive link text** so the purpose of each link is clear from the text alone or from the link text plus immediate context.

### Dates and numbers

- For dates, prefer **day Month year** (e.g. 3 October 2019) unless another format is required for clarity or consistency in a specific context.

## Linting and checks (run before committing)

- **User docs (markdownlint):** `make lint-docs` (runs over `docs/user/**/*.md`).
- **Spellcheck (user docs):** `make spellcheck-docs`; interactive fix: `make fix-spelling`.
- **Excalidraw SVGs:** `make lint-diagrams` ensures SVGs under `docs/` that come from Excalidraw have the scene embedded. Add exceptions via `.excalidraw-ignore` in the same directory if a file is not from Excalidraw.

When you add or edit docs, run the relevant checks before committing: at least `make lint-docs` and `make spellcheck-docs` for user docs; add `make lint-diagrams` if you added or changed Excalidraw SVGs.

## Summary

1. Put **user** content under **docs/user/** and **developer** content under **docs/developer/**.
2. Follow the **Documentation style** section above (tone, voice, minimalism, headings, lists, procedures, code examples, placeholders, admonitions, links, accessibility).
3. Update **docs/user/README.md** or **docs/developer/README.md** when adding new top-level sections or important pages.
4. Use **relative links** and **Markdown**; export Excalidraw SVGs with **Embed Scene**.
5. **Before committing:** Run **`make lint-docs`** and **`make spellcheck-docs`** (and **`make lint-diagrams`** if touching SVGs); fix any failures before committing.
