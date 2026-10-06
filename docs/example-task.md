# Example task (manual runs)

A sample task for trying the agent by hand, e.g.
`go run ./cmd/agent -mission "build the landing page in docs/example-task.md"`.
The explicit mission flag requests a reviewed multi-file execution plan;
without it, direct mode remains active. To use a separate workspace, copy
this task document there and set `-workspace` before the task argument.
Not part of the eval suite — it lives here instead of the repo root so
the root stays free of fixtures.

---

# Test task: mini landing page

Build a minimal static landing page under `./site` with exactly three files.
Each file has a short spec below. Work is done when every item under
"Acceptance" holds.

## site/index.html

- A valid HTML5 document with `<title>Acme Counter</title>`.
- Links `styles.css` and loads `app.js` (defer is fine).
- Body contains exactly:
  `<h1 id="title">Acme Counter</h1>`,
  `<p>Count: <span id="count">0</span></p>`,
  `<button id="btn">Click me</button>`.

## site/styles.css

- `body` has a non-white `background` (any color).
- `#title` has a `color` rule different from the body text color.

## site/app.js

- On `DOMContentLoaded` (or placed after the elements), attaches a click
  handler to `#btn` that increments the number inside `#count` by one.
- No external dependencies, no network calls.

## Acceptance

1. `site/index.html`, `site/styles.css` and `site/app.js` all exist.
2. `index.html` references both `styles.css` and `app.js`.
3. `styles.css` sets a body background and a `#title` color.
4. `app.js` wires `#btn` clicks to increment `#count`.
