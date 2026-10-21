# Recipes

<!-- Generated from the built-in recipes compiled into the binary. Do not edit.
     Regenerate with `aboard recipes index > <this file>`, or `make caps`
     in aboard's own checkout, which also rebuilds the other two. -->

| name | description | when to use | scope |
| --- | --- | --- | --- |
| `apply-a-write` | The read-modify-apply shape every board write takes, plus the id allocator and the upsertTab helper. | Before any write to the board — this is the shape all the other recipes assume. Read it first if you are about to change a tab, and re-read it if a write came back 409. | built-in |
| `ask-for-a-decision` | Ask several questions at once as a form with typed fields, then read the answers back by field id. | When you would otherwise write a paragraph containing three questions. Use it for typed input — a choice, a number, a bit of free text — not for an approval you need on the record. | built-in |
| `ask-to-remove-a-tab` | You cannot delete a tab. Set pendingRemoval with a reason worth reading and let the human answer it. | When a tab is superseded or spent and you want it gone. Never drop it from the array — the server restores it with a generic reason, which is worse for the human than the one you would have written. | built-in |
| `build-something-interactive` | Ship a bespoke sandboxed widget as an html tab when the interaction itself is the point and no renderer covers it. | Only when the INTERACTION is the point — canvas, drag-and-drop, a simulation. Prefer a `ui` tab whenever a component tree can express it: it cannot get the theme wrong and the next session can change one node of it. | built-in |
| `composite-review-tab` | One stack tab holding several renderers — look at this, then decide that — instead of three tabs the human must correlate. | Whenever the evidence and the decision belong together: a graph plus the form that acts on it, a screenshot plus the open questions. This is usually the right shape. | built-in |
| `coordinate-with-another-agent` | Open a chat tab as the channel between sessions, where the human can watch the handoff and interject. | When more than one session is working the same project and they need to divide the work in the open. Also the place to answer a human who has interjected in the transcript. | built-in |
| `point-at-part-of-an-image` | Show an image the human can draw on, then read their regions and strokes back as pixels and name what each one landed on. | When the thing you need them to point at is on a screen — a layout, a chart, a diff of two screenshots. Also when you need to prove you understood the mark they made. | built-in |
| `react-to-their-edits` | Read what the human asked for, then diff the board against the copy you last applied to find what they changed, dismissed or deleted. | At the start of a turn after the human has had the board, or whenever they say "I moved things around". A request is a direct ask; a dismissed marker means they read it; a deleted tab is an answer. | built-in |
| `show-a-structure` | Put a plan or a dependency set on the board as a dag the human can drag into the right shape, and mirror it as a kanban. | When you have inferred a structure — a plan, a dependency graph, an order of work — and want it argued with rather than approved in prose. Also when the same nodes want a second reading by status. | built-in |

**The table above is only the recipes shipped in the binary. This project may
have more.** Run `aboard recipes list` to see every recipe actually available
here — one line per recipe (`name`, `scope`, `description`), and, indented
under it, the file's path and anything it shadows. A clean built-in is one
line; a recipe with something worth knowing about it is two, and says what.
`aboard recipes list --output-format json` carries the whole record, including
`whenToUse`, `tags`, `requires`, `hasTemplate` and `shadowedBy`. It is the only
complete answer, because a project's own recipes are files on disk that no
generated document can know about. `aboard recipes show <name>` prints the
recipe body to stdout; read it and follow it. If the recipe carries a tab
skeleton, `aboard recipes show <name> --template` prints just that JSON, ready to
edit and hand to `aboard apply` — and a recipe with no skeleton exits non-zero
naming itself, rather than printing an empty document you would apply as an
empty tab. When two recipes share a name the first of `_apex/aboard/recipes/` →
`_aboard/recipes/` → `.aboard/recipes/` → built-in wins, and the winning row
names the file it shadowed rather than hiding it — a project that overrides a
built-in recipe is doing something deliberate and you should be able to see
what it replaced.

Beyond the built-ins there is a LIBRARY: recipes collected in aboard's own
repository under `recipes/`, which are not compiled into the binary and are
used by copying the file into one of the three directories above. This table
cannot list them — it is generated from the binary, and it ships inside a
skill directory copied between projects, where a path into somebody else's
checkout would name nothing. In aboard's own checkout, if you have it,
`recipes/README.md` is the library and `docs/reference/recipes.md` is the
format reference: every folder and its precedence, the frontmatter schema,
the `aboard-template` block, the commands, and every reason a file is
rejected.
