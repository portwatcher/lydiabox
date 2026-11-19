# My global rules

- Answer in British English.
- Run the tests before a commit.

<!-- lydiabox:begin (managed by LydiaBox; this block is replaced on each start) -->
## LydiaBox computer and browser use

You run on a LydiaBox VM with a 1920x1080 desktop. The user can watch this
desktop and take it over. For web tasks, use the Google Chrome on this
desktop, so that the user sees what you do. A headless browser is not
visible to the user.

Browser: use `lbx browser <op>` in the shell, or the `browser_*` tools of
the `lydiabox` MCP server. They are the same operations.
- `lbx browser open <url>` opens a page (`--new-tab` for a new tab).
- `lbx browser snapshot` lists the headings and the interactive elements
  with refs such as `[e5]`.
- `lbx browser click e5`, `lbx browser type e7 "text" --submit`,
  `lbx browser fill e7 "text"`, `lbx browser select e9 "Option"`,
  `lbx browser press Enter`.
- `lbx browser text` reads the page text. `lbx browser screenshot` saves a PNG
  and prints its path. `lbx browser eval "<js>"` runs JavaScript.
- `lbx browser tabs`, `tab N`, `close N`, `back`, `forward`,
  `scroll down`, `wait text "Done"`.
Take a new snapshot after the page changes. Refs from an old page do not work.

Desktop (other applications): `lbx computer screenshot` (prints a PNG path),
`lbx computer click X Y`, `type "text"`, `key ctrl+l`, `scroll X Y down 3`,
`launch chrome|blender|terminal`, or the `computer_*` MCP tools.

If an action reports a user takeover, the user controls the desktop. Wait,
then take a new snapshot or screenshot. `lbx browser help` and
`lbx computer help` list all operations.
<!-- lydiabox:end -->
