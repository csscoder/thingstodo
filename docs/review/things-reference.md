# Things 3 Reference: Data Model, List Semantics, Automation APIs

Compiled 2026-10-16 from the official Cultured Code support site (raw HTML fetched with curl and converted to text). Current app at time of check: Things for Mac 3.24.1 (2026-09-30). URL scheme version: 2.

Markers:
- **[verified]** = stated on the cited page (the default; not repeated on every line).
- **[inferred]** = derived from wording, examples or release notes, not stated as a rule.
- **[unverified]** = not found on the support site.

Source abbreviations (all under `https://culturedcode.com/things/support/articles/`):

| Key | Article | URL |
|---|---|---|
| URL | Things URL Scheme | .../2803573/ |
| SC | Things Shortcuts Actions (technical docs) | .../9596775/ |
| AS | Things AppleScript Commands | .../4562654/ |
| ASI | Using AppleScript (intro) | .../2803572/ |
| LISTS | An In-Depth Look at Today, Upcoming, Anytime, and Someday | .../4001304/ |
| SCHED | Scheduling To-Dos | .../2803579/ |
| REP | Using Repeating To-Dos | .../2803564/ |
| PAT | Choosing a Repeat Pattern | .../3743733/ |
| REM | Setting a Reminder | .../2803585/ |
| HEAD | Using Headings in Projects | .../2803577/ |
| TAGS | Using Tags | .../2803581/ |
| NLI | Using Natural Language Input | .../9780167/ |
| QF | Searching and Navigating with Quick Find | .../2803584/ |
| QE | Quick Entry (Mac) | .../2249437/ |
| MOVE | Moving Items in Things | .../9651894/ |
| MAIL | Using Mail to Things | .../2908262/ |
| OTHER | Adding To-Dos From Other Apps | .../2803569/ |
| SYNC | Syncing with Things Cloud | .../2803586/ |
| AI | Third-Party AI Tools and Things (last updated 2026-04-02) | .../5510170/ |
| KBD | Keyboard Shortcuts for Mac | .../2785159/ |
| MD | Markdown Guide | .../4651820/ |
| NOTES | Writing Notes in Things | .../4438545/ |
| TPL | How to Work With Templates | .../2693493/ |
| WAIT | Dealing with Waiting To-Dos | .../2237050/ |
| UNSTUCK | Why Some To-Dos Get Stuck | .../8491676/ |
| WIDG | Using Widgets | .../2803567/ |
| CAL | Connecting Your Calendars | .../2803583/ |
| SIRI | Speaking to Siri | .../2877019/ |
| RN-MAC | Mac Release Notes | .../1100684/ |
| RN-IOS | iPhone / iPad Release Notes | .../2409121/, .../2409117/ |
| BLOG-REP | Blog: "Repeating To-Dos, Refined" (2026-08-19, Things 3.23) | https://culturedcode.com/things/blog/2026/08/repeating-to-dos-refined/ |

---

## 1. Entities and relations

### 1.1 Entity types

| Entity | Notes | Source |
|---|---|---|
| To-do | Title, notes, When (start), reminder, deadline, tags, checklist, status, creation/modification/completion dates. | SC (Item properties) |
| Project | Same date/tag/notes fields as a to-do. No checklist. Contains to-dos and headings. Can be scheduled, put in Someday, completed, repeated. "Projects are just like to-dos." | SC, UNSTUCK |
| Area | Container for projects and to-dos. Has title and tags. No dates, no status, no notes in Shortcuts. Cannot be completed. | SC ("Status … Only available for to-dos, headings, and projects") |
| Heading | Exists only inside projects. Has title, status/completion date, "archived" state, creation/modification date. | HEAD, SC, URL (JSON heading) |
| Checklist item | Belongs to a to-do only. Title plus status open/completed/canceled. | URL (JSON checklist-item), SC |
| Tag | Title, optional parent tag (nesting), optional single-key keyboard shortcut, user-defined order. | TAGS, AS |

Built-in lists (not entities, cannot be renamed, removed or reordered, per SCHED): Inbox, Today, Upcoming, Anytime, Someday, Logbook, Trash (Mac only). Hidden built-in lists, reachable via Quick Find: Tomorrow, Deadlines, Repeating, All Projects, Logged Projects (QF, SC). Each tag also acts as a hidden list showing all items with that tag (SC "List Type").

### 1.2 Allowed relations

| Rule | Source |
|---|---|
| A to-do's parent can be a project or an area, or none. "For a to-do, this can be a project or an area." A to-do can sit directly in an area without a project. | SC (Parent property); URL (`list-id` = "project or area") |
| A to-do with no parent and no date sits in the Inbox (if created without `when`/`list-id`) or is "loose". Anytime shows "loose to-dos without a parent (project or area)" at the top. | URL (add), LISTS |
| A heading's parent is always a project. "Headings are only available in projects. This feature is not available in areas or any other list." | HEAD, SC |
| A to-do may be under a heading (Shortcuts property `Heading`, "Only available for to-dos"). When setting a heading, the parent must be the same project. | SC (Create To-Do: "Parent parameter must be set to the same project the heading is in") |
| A project's parent is an area or none. "For a project, it is its area, if any." Projects "can move in and out of areas". | SC, MOVE |
| An area has no parent. "Not available for areas." Areas cannot be nested in other areas. **[inferred]** No page states this explicitly. | SC |
| Projects inside projects: not supported. **[inferred]** The Move dialog lists only "projects can move in and out of areas". A release note mentions "a crash … when dragging a project into another project", which is not a rule. | MOVE, RN-MAC 3.24.1 |
| Headings can move between projects. Moving a heading moves its to-dos. | MOVE, HEAD |
| A heading can be converted into a project (its to-dos come along). A to-do with a checklist can be converted into a project, and each checklist item becomes a to-do. | HEAD, UNSTUCK |
| Checklist items belong only to to-dos. Projects, headings and areas have none. | SC |
| Tags can be applied to to-dos, projects and areas. Headings cannot be tagged. **[inferred]** from SC ("Tags … Only available for to-dos, projects, and areas"). | SC, TAGS |
| A to-do inside a repeating project cannot have its own repeat rule. A copy generated by a repeating template cannot itself be made repeating. | REP Q&A |

### 1.3 Tags

- Default tags shipped: Home, Office, Errand, Important, Pending. (TAGS)
- **Inheritance:** tagging a project or area makes every item inside inherit the tag. Inherited tags are **not shown** on the to-do (only directly applied tags are), but filtering and search **include** them. (TAGS)
- Shortcuts exposes both: `Tags` (direct only) and `All Matching Tags` (direct + inherited). (SC)
- **Nesting:** "drop one tag on top of another to nest it" (Mac), "Group" (iOS). AppleScript: `set parent tag of homeTag to tag "Places"`. (TAGS, AS)
- Whether filtering by a parent tag also matches child tags: **[unverified]**.
- **Order:** user-defined. It controls the order in the tag filter bar. (TAGS)
- **Tag shortcuts (Mac):** assign a single key per tag in Window → Tags. `Ctrl+key` toggles the tag on the selected items. `Ctrl+Opt+key` filters the current list by that tag. (TAGS, KBD)
- Renaming a tag applies everywhere. Deleting a tag removes it from items. Removing a tag from an item does not delete the tag. (TAGS, AS)

### 1.4 Notes

- Notes are plain text with Markdown always visible: headings `#`–`###`, `*italic*`/`_`, `**bold**`/`__`, `::highlight::`, `~~strike~~`, `> quote`, `-`/`*`/`+`/`•` lists, `1.` lists, task lists `- [ ]` / `- [x]` / `- [~]` (canceled), `` `code` ``, fenced code blocks (no highlighting), `[Link](url)`, `---`. (MD)
- Styling is not rendered in titles of to-dos, projects or areas. (NOTES)
- No images or attachments. Files dropped from Finder become `file://` links. (NOTES, OTHER)
- Limits:
  - URL scheme: notes max 10,000 characters; other strings max 4,000. (URL)
  - Mail to Things: truncates at 4,000 characters. (MAIL)

---

## 2. Lists and views

### 2.1 Core model: two orthogonal axes

"In Things, you can look at your to-dos from two different perspectives: … context, provided by areas and projects … [and] when you are going to do them." (LISTS)

"Every to-do belongs to one of the following lists: Today …, Upcoming, Anytime, Someday." (SCHED) So the When state is a single-valued field. Shortcuts models it as `Start ∈ {On Date, Anytime, Someday}` plus `Start Date`, `Evening`, `Reminder Date`. (SC)

The derivation implied by the docs (**[inferred]** as a function; each piece is cited):

| Start | Start Date | Effective list |
|---|---|---|
| Inbox flag (`Is Inbox`) | — | Inbox (SC: "Is Inbox … Only available for to-dos") |
| On Date | ≤ today | Today. Evening=true puts it in the This Evening section. |
| On Date | > today | Upcoming |
| Anytime | — | Anytime |
| Someday | — | Someday |

Today and Anytime overlap: Today items also appear in Anytime with a yellow star (see 2.4). Upcoming, Anytime and Someday are "mutually exclusive" (LISTS).

### 2.2 Inbox

- "Temporary staging ground for your unprocessed thoughts." (LISTS)
- Items arrive via Mail to Things, Quick Entry, the share sheet, or the URL `add` with neither `when` nor `list-id`. Shortcuts Create To-Do with neither Parent nor Start also lands in the Inbox. (LISTS, URL, SC)
- When a to-do is moved to a list or given a future date, it leaves the Inbox. A temporary section appears at the bottom: "x to-dos were moved out of the Inbox". It disappears when you navigate away. (LISTS)
- Moving to Today: probably also leaves the Inbox. **[inferred]** Docs mention only "moved to a list, or assigned it a future date".

### 2.3 Today and This Evening

- **Rule:** "Think of it as a filter across the entire app: if the start date, deadline, or repeating rule of any to-do matches today's date, it shows up here." (LISTS)
- Calendar events (Apple Calendar only, per device, not synced) appear at the top when enabled. (LISTS, CAL)
- **Order:** user-defined via drag and drop. Optional setting "group by project or area" (Things → Settings → General), which syncs. (LISTS)
  - When grouped, Today honors the "Next Action Steps" setting. **[inferred]** from RN-MAC (old note: "The Today list now utilizes the 'Next Actions Steps' setting when 'Group by Project and Area…' is enabled").
- **This Evening:** a section at the bottom of Today. "Still present, so you know there's more to do." (LISTS)
  - The Evening flag only applies when the start date is today. Shortcuts: "Evening … Only applicable if Start is set to On Date and Start Date is today's date"; Create: "Ignored if Start Date is not set to today's date". (SC)
  - URL: `when=evening` and `when=evening@6pm` (This Evening with a 6 PM reminder). (URL)
- **Overdue start dates:** items whose start date has passed stay in Today until rescheduled or completed. Evidence: "If you aren't able to finish everything, you can easily reschedule the remaining items" (LISTS), and for daily fixed-schedule repeats, "If you miss a day, you'll end up with multiple copies stacking up in Today" (PAT). **[inferred]** as a general rule.
- **Deadlines:**
  - A to-do appears in Today when its deadline is today: "matches today's date" (LISTS). RN-MAC also references "a to-do that appeared in Today because of a deadline".
  - Overdue deadlines also show in Today. RN-MAC: "could sometimes cause due/overdue items to be removed from Today when changing their deadline date". **[inferred]** from release notes.
  - A deadline due today is shown in red. **[inferred]** from RN-MAC 3.23.x: "a deadline due today wasn't shown in red in an expanded to-do".
- **"New in Today" yellow dot:** items that just arrived in Today show a yellow dot. **[inferred]** from RN-MAC ("still has its yellow 'new in Today' dot").
- **When the scheduled date arrives:** "To-dos (and projects) with future start dates hibernate in Upcoming until their start date arrives. Then they automatically move over to Today." (SCHED, LISTS)

### 2.4 Anytime

- Contains only **active** to-dos: "nothing's blocking you". It is mutually exclusive with Upcoming and Someday. (LISTS)
- Includes:
  - Items with deadlines (unless they have a future start date). "Unlike tasks with future start dates, those with deadlines will remain in Anytime." (LISTS, SCHED)
  - Today's items, shown with a yellow star. (LISTS)
- **Order and grouping:** "loose to-dos without a parent (project or area) float at the top, and then to-dos are grouped under their direct parent list." Projects or areas with no active to-dos are omitted. (LISTS)
- **Item limit:** Anytime shows a limited number of to-dos per group with a "Show # more items" button. This is controlled by "Next Action Steps" in Anytime's View menu. **[inferred]** from RN-MAC. The exact default count is **[unverified]**.
- The Clear button in the When picker removes any schedule and assigns the task to Anytime. (SCHED)

### 2.5 Upcoming

- "Future agenda". Shows scheduled to-dos, repeating to-dos, deadlines and calendar events. (Features page, LISTS)
- **Grouping:**
  - "At the top of Upcoming, you'll see the next seven days listed separately, starting with tomorrow." (LISTS)
  - Later dates are grouped in larger ranges (month ranges). **[inferred]** from RN-MAC ("incorrect month ranges could be shown in Upcoming"). The exact buckets (rest of month, then months, then years) are **[unverified]**.
- Order within a date: AppleScript says "to-dos are sorted by start date, just like in Things' UI". Order within the same day is **[unverified]**. (AS)
- Dragging a to-do onto another day changes its start date. (LISTS, gestures)
- **Projects with future start dates** "disappear from the sidebar … while they're in hibernation mode". They reappear when the date arrives. Inside projects, "inactive to-dos take a back seat beneath their active counterparts" (shown under "later items", toggled with ⇧⌘E). (LISTS, KBD)
- Future deadlines also appear in Upcoming. **[inferred]** from RN-MAC ("prevent some to-dos with a future deadline from appearing in Upcoming").
- **Repeating items:** "Upcoming is … organized by when you'll start them, when they have deadlines, or when they'll repeat next." (Guide). How far ahead future repeat occurrences are shown: **[unverified]**. Only "the next copy" is described anywhere.

### 2.6 Someday

- "No plan for it yet." Neither to-dos nor projects in Someday appear in Anytime or Upcoming. "Inactive projects disappear from the sidebar". They are visually de-emphasized. (LISTS)
- **To-dos inside a Someday project:** effectively inactive; the whole project is "on hold". **[inferred]** from WAIT ("If an entire project is on hold, send the whole project there… gets stuck items out of Today and Anytime") and TPL (a template project parked in Someday becomes "inactive").
- **Someday to-dos inside active projects:** the Someday list has an "Active Projects" section showing those to-dos (expanded by default). **[inferred]** from RN-MAC.
- A Someday item with a deadline is possible. **[inferred]** from RN-MAC ("dragging a Someday item with a deadline").

### 2.7 Logbook

- Archive of completed **or** canceled to-dos and projects. Items stay indefinitely. (LISTS)
- Logged projects: Quick Find "Logged Projects". (LISTS, QF)
- Clearing: select all and delete. On iPhone, iPad and Vision deletions are permanent. On Mac, items go to the Trash. (LISTS)
- **Logging interval setting** exists with values "Immediately", "Daily" and "Manual".
  - Evidence: RN-IOS ("changing the logging interval to 'Immediately'", "if the logging interval was set to 'manual' or 'daily'"), RN-MAC ("if Logging was set to Manual or Daily", "logging was not set to immediately"), and AS ("If Things' is set to log Manually, use the `log completed now` command").
  - The exact Settings path and the precise time "Daily" runs are **[unverified]**. Release notes mention "logging on app launch", so Daily may run on launch or day change. **[inferred]**
  - Manual logging: Mac shortcut ⇧⌘Y "Move completed to Logbook". (KBD)
- **Completed is not the same as Logged:** Shortcuts: "Is Logged: Return items that have moved to the Logbook. This is not necessarily all items that have been completed." (SC)
- **Headings:** archiving a heading moves it "to the logged items at the bottom of the project". If a to-do under an archived heading is set back to Open via Shortcuts, "the to-do will be marked incomplete but will stay in the Logbook. This is the intended behavior." (HEAD, SC)
- **Projects:** marking a project incomplete restores its completed to-dos from the Logbook. A project's to-dos should not show independently in the Logbook once the project is logged. **[inferred]** from RN-MAC bug fixes.

### 2.8 Trash

- Mac only. "iPhone, iPad, and Vision don't have a Trash at this time." (SC)
- AppleScript `delete`:
  - To-do: moves to the Trash.
  - Project: moves the project and its children to the Trash.
  - Area: "is not moved to the Trash, but its children will be". (AS)
- `empty trash` command. (AS)
- Trash order: most recently deleted at the top. **[inferred]** from RN-MAC ("Restored the correct sort-order for items in the Trash").
- Items can be dragged out of the Trash to restore them. **[inferred]** from RN-MAC.
- No auto-expiry is documented. **[unverified]**

### 2.9 Special Quick Find lists

| List | Content | Source |
|---|---|---|
| Tomorrow | Everything scheduled for the next day. | QF, SCHED |
| Deadlines | All items with a deadline, chronological (including passed ones: "all your upcoming (or passed!) deadlines, neatly sorted"). | SCHED, RN-MAC 3.11 |
| Repeating | Every repeating template plus upcoming copies (see 4.5). | QF, BLOG-REP |
| All Projects | Every open project, including future and Someday ones behind "Show x later items". | QF, TPL |
| Logged Projects | Completed projects in the Logbook. | QF |

These lists cannot be pinned to the sidebar. They are available as widgets, Shortcuts lists and URL `show` ids. (QF, SC, URL)

### 2.10 Tag filtering in lists

- Every list has a tag filter bar that only offers tags used in the current list. (TAGS, Prioritizing article 3289315)
- Multi-tag filter = **AND** ("items in the list that contain all the tags you've selected"). (TAGS)
- Inherited tags count for filtering and search. (TAGS)
- A tag search in Quick Find opens a dedicated tag list showing all items with that tag, "grouped by parent list". (Prioritizing 3289315, TAGS)
- Someday items are also affected by tag filtering inside projects and areas. **[inferred]** from RN-MAC.
- URL: `show?id=…&filter=tag1,tag2`. Shortcuts: Open List with "Filter by Tags". (URL, SC)

### 2.11 Quick Find search scope

- Default: names of to-dos, projects, areas, tags, default lists and headings. (QF, gestures)
- "Continue Search" adds notes, checklists and completed items from the Logbook. (QF)

---

## 3. Dates

### 3.1 When (start date)

- Semantics: "a start date in Things merely indicates the day on which you want to begin work on a to-do". It is not a calendar slot. (SCHED)
- Values: Today, This Evening, a specific date (goes to Upcoming if in the future), Anytime (Clear), Someday. (SCHED)
- Shortcuts: `Start Date` time component "always set to midnight". (SC)
- Mac keyboard: ⌘T Today, ⌘E This Evening, ⌘R Anytime, ⌘O Someday, Ctrl+]/[ ±1 day, Ctrl+Shift+]/[ ±1 week. (KBD)
  - If an item has a deadline but no When, Ctrl+[ sets a When relative to the deadline. **[inferred]** from RN-MAC.
- Projects have When too. Future-dated projects hibernate (see 2.5).

### 3.2 Reminders

- "A reminder is set via When." It is a time on the start date. Shortcuts: "Reminder Date … date component is equal to the item's start date, and the time component is set to when the reminder will fire." (REM, SC)
- Deadlines cannot have reminders. (REM, NLI)
- Not persistent; not an alarm. (REM)
- Snooze: the support article says 15 min, 1 h, 3 h, or until the next day. RN-MAC 3.24 says "Improved snoozing options … more suitable durations, and a Tomorrow option", so the article list may be stale. **[flag]**
- Lists cannot be sorted by reminder time. (REM)
- Things Cloud "Fastlane" pushes newly set reminders to other devices instantly. (Features page)
- Setting a reminder on a to-do in Today does not move it to This Evening. This was a fixed bug, which implies the intended behavior is independence. **[inferred]**

### 3.3 Deadlines

- "Deadlines define the date by which you must finish the to-do." Tasks with a deadline remain active in Anytime. To see a task earlier, add a start date before the deadline. (SCHED)
- Date only. Shortcuts: "time component is always set to midnight"; Create: "Does not support a time value". (SC)
- Today inclusion and overdue display: see 2.3. The Deadlines list includes passed deadlines.
- A start date equal to the deadline date is allowed. **[inferred]** from RN-MAC.

### 3.4 Natural language (UI)

- Languages: English, German, French, Italian, Spanish, Russian, Chinese, Japanese. (NLI)
- Examples (NLI):

| Input | Meaning |
|---|---|
| `tod…` | Today |
| `10-3` | October 3 (format follows the device region) |
| `10-3 9` | 9 AM/PM on October 3 |
| `2p` | 2 PM reminder |
| `17d` | 17 days from today (`w` = weeks, `mo` = months, `y` = years) |
| `17d from jul9` or `jul9 + 17d` | 17 days after July 9 |
| `wed jul…` | Suggestions for Wednesdays in July |
| `3rd wed jul`, `3rd wed jul 2026` | Third Wednesday in July |
| `last fri jul` | Last Friday in July |
| `tues 8am` | Tuesday with an 8 AM reminder |

- Partial words are fine. Any relative date works in both When and Deadline.
- A time attaches only to the start date, never to the deadline.
- Natural language for Deadlines: Mac, and iPad or Vision with a hardware keyboard only.
- **URL scheme:** dates must be English regardless of device language. It accepts `today`, `tomorrow`, `yyyy-mm-dd` and natural language such as `in 3 days` or `next tuesday`. (URL)

---

## 4. Repeating to-dos and projects

### 4.1 Template and copy model

- "Behind every repeating to-do or project is a template. It contains the repeating rule and the title, notes, tags, and everything else." Every instance is a copy made from the template. (REP)
- Edits to a copy do not propagate. Edit the template to change future copies. (REP)
- In the UI the template has a blue circular arrow. Copies show "This is a copy of a repeating to-do" (a link to the template) and, since 3.23, a small grey repeat icon. (REP, BLOG-REP)
- Checklist items cannot be completed in the template, only in a copy. (REP)

### 4.2 Patterns (PAT)

- **After completion:** the next copy is created after you check off the current one, offset from the **completion** date. Example: due on Wednesday, done on Saturday, so the next copy comes the following Saturday.
  - When created, the first copy appears in Today immediately. Reschedule that copy to delay the cycle.
  - iOS has a "Start Early" value of up to 100 days for after-completion rules. (RN-IOS)
- **Regular (daily / weekly / monthly / yearly):** copies are created on schedule regardless of completion. Missed daily copies stack up in Today.
  - Regular-only options: specific weekdays or days (e.g. Mon–Fri, "every 3rd Tuesday each month"), and an end date. (PAT, REP)

### 4.3 Deadlines and reminders on repeats

- Reminder: a time on the day the copy appears. (REP)
- "Add deadlines": the deadline always falls on the repeat date. The "days earlier" field makes the copy appear N days before.
  - Example: repeat on the 15th, 5 days earlier, so the copy appears on the 10th with deadline the 15th. (REP)
  - Shortcuts quirk: for such a copy, the `Deadline` property returns the appearance date, not the final deadline. The offset lives in the template and is not exposed. (SC)

### 4.4 Controls (REP, BLOG-REP; Things 3.23, 2026-08)

| Control | Behavior |
|---|---|
| Edit Rule | Change the pattern. |
| Create Next Copy | Generate the next copy early so you can work on it ahead of time. Also available for repeating projects. |
| Show Previous Copy | Jump to the previous copy. |
| Pause / Resume | Stop and restart generating copies. |
| Stop | The template becomes a regular to-do. |
| Batch | Pause, resume and stop work on a multi-selection. Rule editing does not. |

- **Complete early:** check off the template or a scheduled copy in Upcoming. "Things will then schedule the next copy based on its existing rule." Before 3.23 this was impossible.
- **Reschedule a copy:**
  - Fixed schedule: choose **Make Exception** (only this copy moves) or **Update Rule** (the rule shifts; e.g. moving a Monday copy to Tuesday makes it repeat every Tuesday).
  - After completion: the copy simply moves.

### 4.5 Repeating list (REP)

- Shows templates and their upcoming copies.
- Loose items at the top, then grouped under the parent project or area.
- Ordered by next occurrence, soonest first.
- Paused and "waiting" templates at the bottom.

### 4.6 How many future instances exist

Only "the next copy" is documented (e.g. "the next copy of this to-do will be scheduled for Wednesday next week" in Upcoming, PAT). Whether Upcoming projects multiple future occurrences: **[unverified]**.

### 4.7 Templates (non-repeating)

There is no template feature. The recommended workaround: a project tagged "Template", parked in Someday, duplicated when needed. Dates do not shift on duplicate. (TPL)

---

## 5. Completion, cancellation, logging, deletion

- **Status values:** Open, Completed, Canceled. This applies to to-dos, headings (Shortcuts property) and projects. Checklist items also have open, completed and canceled. (SC, URL, MD)
- **Cancel:** ⌥⌘K or Opt+click the checkbox. Complete: ⌘K. (KBD)
- Completed and canceled to-dos and projects both go to the Logbook (subject to the logging interval). (LISTS)
- **Project completion rules differ by API.** These are documented differently, so they are recorded literally:

| API | Rule |
|---|---|
| URL `add-project completed=true` | "Will set all child to-dos to be completed" (`canceled=true` cancels all). |
| JSON project `completed` / `canceled` | "Ignored unless all child to-dos are completed or canceled." |
| URL `update-project completed` / `canceled = true` | "Ignored unless all child to-dos are completed or canceled **and all child headings archived**." |

- UI behavior when completing a project with open to-dos (prompt? cascade?): **[unverified]**.
- **Headings:** "archived" (JSON `archived`: "Ignored unless all to-dos under the heading are completed or canceled"). (URL)
- **Reopening:**
  - URL: `completed=false` on a canceled item also reopens it; `canceled=false` on a completed item also reopens it. (URL)
  - Shortcuts: reopening a to-do under an archived heading leaves it in the Logbook. (SC)
- **Deletion:** see 2.8. Shortcuts Delete Items:
  - Deleting a project deletes everything inside. Deleting a heading deletes the items under it. Areas are ignored.
  - "Delete Immediately" must be ON on iOS. (SC)
- **Creation, modification and completion dates** are stored. URL and Shortcuts allow setting creation and completion dates in the past; future dates are ignored. (URL, SC)

---

## 6. Automation surfaces (prior art for an AI-first API)

Officially "safe" integration methods: URL scheme, Apple Shortcuts, AppleScript (Mac), Mail to Things. Direct database writes and tools asking for Things Cloud credentials are "not safe" and have caused data loss. There is no direct ChatGPT, Claude or Gemini integration; third-party MCP servers exist and should use only the safe methods. (AI, last updated 2026-04-02)

### 6.1 Things URL scheme (version 2): general rules (URL)

- **Form:** `things:///command?param=value&…`. Values are percent-encoded.
- **x-callback-url:** all commands support `x-success`, `x-error` and `x-cancel`. Many return parameters to `x-success`.
- **Authorization:** commands that modify existing data (`update`, `update-project`, `json` containing an update operation) require `auth-token`. The token is per user, from Settings → General → Enable Things URLs → Manage (Mac) or Settings → General → Things URLs (iOS).
- **Enablement:** the first URL run prompts the user to enable the scheme.
- **Rate limit:** "a limit of 250 items can be added within a 10 second period" (stated under `add`).
- **IDs:** obtained via Share → Copy Link on an item or list. Example IDs in the docs are 22-character base62-like strings (e.g. `SyJEz273ceSkabUbciM73A`).
- **Clearing a value:** "Including a parameter with an equals sign (=) but without a value will clear that value" (e.g. `deadline=`). This is documented for `update` and `update-project`.

**Data types:**

| Type | Definition |
|---|---|
| string | Percent-encoded. Max 4,000 unencoded characters unless stated otherwise. |
| date string | `today`, `tomorrow`, `yyyy-mm-dd`, or English natural language (`in 3 days`, `next tuesday`). |
| time string | Local time, e.g. `9:30PM` or `21:30`. |
| date time string | `<date string>@<time string>`, e.g. `2026-02-25@14:00`. |
| ISO8601 date time string | e.g. `2026-03-10T14:30:00Z` or `+01:00`. Used for creation and completion dates. |
| boolean | `true` / `false`. |
| JSON string | — |

### 6.2 `add` (create to-do; no auth)

All parameters are optional. "If neither the `when` nor `list-id` are specified, the to-do will be added to the inbox." (The `list` title variant is not mentioned in that sentence.)

| Param | Type | Rule |
|---|---|---|
| `title` | string | Ignored if `titles` is given. |
| `titles` | `%0a`-separated strings | Creates several to-dos. Takes priority over `title` and `show-quick-entry`. Other params apply to all. |
| `notes` | string | Max 10,000. |
| `when` | string | `today`, `tomorrow`, `evening`, `anytime`, `someday`, a date string, or a date time string (adds a reminder). Time is ignored with `anytime`/`someday`. |
| `deadline` | date string | — |
| `tags` | comma-separated titles | **Tags that don't exist are not applied (silently).** |
| `checklist-items` | `%0a`-separated | Max 100. |
| `use-clipboard` | `replace-title` (newlines overflow into notes) / `replace-notes` / `replace-checklist-items` | Takes priority over `title`/`notes`/`checklist-items`. |
| `list-id` | string | Project or area ID. Takes precedence over `list`. |
| `list` | string | Project or area **title**. Ignored if `list-id` is present. |
| `heading-id` | string | Takes precedence over `heading`. Ignored if no project is specified or the heading doesn't exist. |
| `heading` | string | Heading title. Same ignore rules. |
| `completed` | bool | Default false. Ignored if `canceled=true`. |
| `canceled` | bool | Default false. Takes priority over `completed`. |
| `show-quick-entry` | bool | Show a prefilled Quick Entry instead of creating. Ignored if `titles` is given. |
| `reveal` | bool | Navigate to the created to-do (the first one if several). Ignored with `show-quick-entry`. |
| `creation-date` | ISO8601 | Ignored if in the future. |
| `completion-date` | ISO8601 | Ignored if the to-do is not completed/canceled, or the date is in the future. |

`x-success` returns `x-things-id`: comma-separated IDs of the created to-dos.

Examples:
- `when=evening@6pm`
- `titles=Milk%0aBeer%0aCheese&list=Shopping`
- `when=next%20monday&list-id=…`

### 6.3 `add-project` (no auth)

| Param | Rule |
|---|---|
| `title`, `notes` (10,000) | — |
| `when` | Same values as `add`. |
| `deadline` | Date string (example uses `December%2031`). |
| `tags` | Comma-separated. Unknown tags are dropped. |
| `area-id` | Takes precedence over `area`. |
| `area` | Area title. |
| `to-dos` | `%0a`-separated titles of to-dos to create inside the project. |
| `completed` / `canceled` | Cascade to all child to-dos. `canceled` wins. |
| `reveal` | Navigate into the project. |
| `creation-date`, `completion-date` | ISO8601. Also applied to the `to-dos` children. |

There is no headings parameter (use `json`). `x-success` returns `x-things-id` (the project ID).

### 6.4 `update` (to-do; requires `id` + `auth-token`)

| Param | Rule |
|---|---|
| `id` | Required. |
| `auth-token` | Required. |
| `title` | Replaces. |
| `notes` | Replaces. Max 10,000. |
| `prepend-notes`, `append-notes` | Max 10,000. |
| `when` | `today`, `tomorrow`, `evening`, `someday`, a date string, or a date time string (adds a reminder). **`anytime` is NOT listed** for update, unlike `add`. Time is ignored with `someday`. **Cannot be updated on repeating to-dos.** Clearing via `when=` (moves to Anytime?) is not stated: **[unverified]**. |
| `deadline` | `deadline=` clears it. Cannot be updated on repeating to-dos. |
| `tags` | Replaces all tags. Unknown tags are dropped. |
| `add-tags` | Adds tags. Unknown tags are dropped. |
| `checklist-items` | Replaces all. Max 100. |
| `prepend-checklist-items`, `append-checklist-items` | Max 100. |
| `list-id` / `list` | Move into a project or area. |
| `heading-id` / `heading` | Move under a heading. Ignored if the to-do is not in a project with that heading. Can be combined with `list`/`list-id`. (`heading-id` support for `update` was added in a later release, per RN-MAC; the date was not checked.) |
| `completed` | Reopens a canceled item when false. Not allowed on repeating to-dos. |
| `canceled` | Takes priority. Reopens a completed item when false. Not allowed on repeating to-dos. |
| `reveal` | — |
| `duplicate` | Duplicate first, then update the copy, leaving the original untouched. Repeating to-dos cannot be duplicated. |
| `creation-date` | ISO8601. |
| `completion-date` | ISO8601. Not allowed on repeating to-dos. |

`x-success` returns `x-things-id`. There is no way to remove a to-do from its project or area via URL (no "no parent" value): **[unverified]**.

### 6.5 `update-project` (requires `id` + `auth-token`)

Parameters: `id`, `auth-token`, `title`, `notes`, `prepend-notes`, `append-notes`, `when` (no `anytime`; not on repeating projects), `deadline` (not on repeating; `deadline=` clears), `tags` (replace), `add-tags`, `area-id`/`area` (move), `completed`/`canceled` (see the rule in section 5: all children must be done **and** all headings archived; not on repeating), `reveal`, `duplicate` (not for repeating projects), `creation-date`, `completion-date`.

`x-success` returns `x-things-id`.

### 6.6 `show` (navigation only)

- `id`: an area, project, tag or to-do ID, or a built-in id: `inbox`, `today`, `anytime`, `upcoming`, `someday`, `logbook`, `tomorrow`, `deadlines`, `repeating`, `all-projects`, `logged-projects`. Takes precedence over `query`.
- `query`: name of an area, project, tag or built-in list. Equivalent to typing it in Quick Find and taking the first result. **Cannot show a task by query.**
- `filter`: comma-separated tag titles.
- Returns nothing. There is no `trash` id.

### 6.7 `search`

`query` is optional. It opens the search UI. Returns nothing.

### 6.8 `version`

Returns `x-things-scheme-version` and `x-things-client-version` (build number).

### 6.9 `json` (bulk create/update)

**Parameters:**
- `data` (JSON array)
- `auth-token` (required if any operation is an `update`)
- `reveal` (shows the first created item)

`x-success` returns `x-things-ids`: a JSON array of IDs of the **top-level** created to-dos and projects. IDs of to-dos created inside projects are **not** returned. `add-json` is deprecated. The official Swift helper is "Things JSON Coder" (GitHub).

**Operation object:**

```json
{ "type": "to-do" | "project" | "heading" | "checklist-item",
  "operation": "create" | "update",   // default create; update only for to-do and project
  "id": "…",                          // required for update
  "attributes": { … } }               // required key; all attributes optional
```

**To-do attributes:**

| Attribute | Rule |
|---|---|
| `title` | — |
| `notes` | Max 10,000. |
| `when` | `today`, `tomorrow`, `evening`, `anytime`, `someday`, a date string, or a date time string. |
| `deadline` | Date string. |
| `tags` | **Array** of tag titles. Unknown tags are dropped. |
| `checklist-items` | Array of checklist-item objects. Max 100. |
| `list-id` / `list` | Ignored when nested inside a project's `items`. |
| `heading-id` / `heading` | Ignored if no project is given, the heading doesn't exist, or the to-do is nested in `items`. |
| `completed`, `canceled` | — |
| `creation-date`, `completion-date` | ISO8601. |
| `prepend-notes`, `append-notes` | Update only. |
| `add-tags` | Update only. Documented as a **comma-separated string**, not an array. |
| `prepend-checklist-items`, `append-checklist-items` | Update only. `\n`-separated strings, not objects. |

**Project attributes:**

| Attribute | Rule |
|---|---|
| `title`, `notes` (10,000), `when`, `deadline` | — |
| `tags` | Array. |
| `area-id` / `area` | — |
| `completed` / `canceled` | Ignored unless all children are done. |
| `creation-date`, `completion-date` | — |
| `items` | **Create only.** Array of to-do and heading objects. "To add to-dos to an existing project, create individual to-do objects instead." |
| `prepend-notes`, `append-notes`, `add-tags` | Update only. |

**Heading attributes:**
- `title`
- `archived` (bool; ignored unless all to-dos under it are done)

To-dos that follow a heading object in `items` are placed under that heading, per the official example. **[inferred]**

**Checklist-item attributes:**
- `title`
- `completed`
- `canceled` (wins over `completed`)

**Encoding:** strip whitespace, then URL-encode. Example: `things:///json?data=%5B%7B%22type%22:%22to-do%22,%22attributes%22:%7B%22title%22:%22Buy%20milk%22%7D%7D%5D`.

The JSON format has no repeat rule, reminder-only, area-creation or tag-creation fields.

### 6.10 Apple Shortcuts actions (SC)

Minimum: Things 3.17; macOS 14 / iPadOS 17 / iOS 17 / visionOS 26.

**Custom types:**

- **Item:** to-do, heading, project or area. Properties:
  - Type, Title, ID (all types).
  - Parent and Parent ID: to-do → project or area; heading → project; project → area; none for areas.
  - Heading (to-dos only).
  - Is Inbox (to-dos).
  - Start {On Date, Anytime, Someday}, Start Date (midnight), Evening, Reminder Date (date+time), Deadline (midnight). To-dos and projects only.
  - Tags (direct) and All Matching Tags (plus inherited). To-dos, projects and areas.
  - Status {Open, Completed, Canceled}, Completion Date, Is Logged, Creation Date, Modification Date. To-dos, headings and projects.
  - Notes (to-dos and projects).
  - Checklist (to-dos; newline-separated).
- **List:** the built-in visible lists (Inbox, Today, Upcoming, Anytime, Someday, Logbook), the hidden ones (Tomorrow, Deadlines, Repeating, All Projects, Logged Projects), every area, every project, and every tag. Used only by Open List.

**Actions:**

| Action | Parameters / behavior | Returns |
|---|---|---|
| Create To-Do | Title, Parent, Heading (Parent must be the heading's project), Start (+ Start Date with NL and no time, Evening (only if today), Reminder Time `8:15pm`/`20:15`), Deadline (NL, no time), Tags, Status, Notes, Checklist (newlines; status via Markdown `- [ ]`, `- [x]`, `- [~]`), Show When Run. No Parent and no Start puts it in the Inbox. | Item |
| Create To-Do with Quick Entry | Same, but opens Quick Entry. Heading is not supported (bug). | Nothing |
| Create Heading | Title, Project. | Item |
| Create Project | Title, Area, Start (+ Start Date, Evening, Reminder Time), Deadline, Tags, Status, Notes. Add to-dos with subsequent Create To-Do actions. | Item |
| Find Items | Filters: Type, Title (contains), Parent, Is Inbox / Not Is Inbox, Start, Start Date (range), Evening / Not Evening, Reminder Date, Deadline, Tags, All Matching Tags, Status, Completion Date, Is Logged / Not Is Logged, Notes (contains), Creation Date, Modification Date, ID, Parent ID. Sort: Title, Start Date, Reminder Date, Deadline, Completion Date, Creation Date, Modification Date, Random. Limit. **Max 500 results.** Cannot filter for "no deadline", "no parent" or "no tags". | Items |
| Get Items | Type + Items picker. | Items |
| Get Selected Items | Selection in the last active window (not the Mac sidebar). | Items |
| Edit Items | Mode: Set, Append/Prepend (title/notes/checklist), Add/Remove/Remove All (tags). Detail: Title, Parent, Start (NL), Reminder Time (keeps the start date), Deadline, Tags, Status, Completion Date, Notes, Checklist, Creation Date. Not undoable. Warns above 99 items. | Items |
| Duplicate Items | To-dos, headings, projects. Areas are not supported. | Items |
| Delete Items | To-dos, headings, projects. Areas are ignored. Cascades to children. "Delete Immediately" OFF = Trash (Mac); must be ON on iOS. Warns above 99. | — |
| Open List | List + Filter by Tags. | — |
| Show Items | One item is shown in its parent; several are shown in a special list. | — |
| Run Things URL | Runs URL scheme commands in the background. Not for commands that need the foreground (e.g. `show`). | — |

Shortcuts cannot create areas or tags, and cannot create or edit repeat rules. **[inferred]** There is no such action or Edit detail.

### 6.11 AppleScript (Mac only; AS, ASI)

**Basics:**
- App name `"Things3"`.
- Collections: `to dos`, `projects`, `areas`, `tags`, `lists`.
- Built-in lists: `list "Inbox"`, `"Today"`, `"Anytime"`, `"Upcoming"`, `"Someday"`, `"Logbook"`, `"Trash"`. In a non-English UI, use the localized name.
- Collection order matches the UI order.

**Create:**
- `make new to do with properties {name:…, notes:…, due date:…, tag names:"A, B"} at beginning of list "Today" | project "X" | area "Y"`
- `make new project`, `make new area`, `make new tag`

**Properties:**
- `name`, `notes`, `due date` (= **deadline**, not start), `completion date`, `cancellation date`, `creation date`, `modification date`
- `status` (open/completed/canceled), `tag names` (comma-separated string), `tags`, `area`, `project`
- Tag property: `parent tag`

**Scheduling:**
- `schedule toDo for <date>` sets the start date.
- "You cannot move a to-do or project to Upcoming" with `move`; use `schedule`.
- `move x to list "Today"` / `"Logbook"`.

**Status:** `set status of x to completed` or `canceled`.

**Detach from a parent:** `delete project of toDo`, `delete area of aProject`.

**Delete:**
- `delete` sends to-dos and projects (with their children) to the Trash.
- Deleting an area: the area itself is not trashed, but its children are.
- `empty trash`.

**UI and other commands:**
- `show`, `edit` (not for areas)
- `selected to dos`
- `show quick entry panel [with properties {…}] [with autofill yes]`
- `log completed now` (when logging is Manual)
- Clear a value: set it to `""`.

**Not available:** headings and checklists ("editing headings and checklists" requires Shortcuts). "If it's not documented here, it's not possible."

### 6.12 Mail to Things (MAIL)

- A private `@things.email` address (requires Things Cloud). Subject → title, body → notes. Lands in the Inbox.
- No tags or metadata. Truncates at 4,000 characters. Plain text (Markdown kept). Attachments are ignored.
- **Max 100 emails per 24 h.** The address can be regenerated (the old one dies immediately).

### 6.13 Other input paths

- **Paste:** each line becomes a to-do. Max 100 rows per paste. Bullets are stripped. Pasting in the title field: the first line becomes the title, the rest goes to notes. Pasting in a checklist: each line becomes an item. (OTHER)
- **Siri:** regular Siri, plus Siri AI (OS 27 beta). Siri AI knows only one date per to-do (the start date): no deadlines, no "move to Today". (SIRI)
- **Widgets:** a list, project, area, Quick Find list or tag, optionally plus a tag filter. (WIDG)
- **Import:** from Apple Reminders, Todoist and OmniFocus; Microsoft To Do one by one. (2803555)

---

## 7. Quick Entry, sync, limits

### 7.1 Quick Entry (Mac; QE)

- Ctrl+Space opens Quick Entry. Ctrl+Opt+Space opens Quick Entry with Autofill, which adds a link to the current Safari page, Mail message or Finder file into notes and needs a helper app. Both are customizable.
- All main-app shortcuts work: When (⌘S, with natural language), deadline (⇧⌘D), tags (⇧⌘T), Move (⇧⌘M, which can target headings).
- The default destination button reads "Inbox". (QE, MOVE)
- Esc discards. ⌘Return saves.
- iOS: drag the Magic Plus to the Inbox target for an Inbox Quick Entry. (gestures 2803582)
- Natural-language rules: see 3.4.

### 7.2 Things Cloud (SYNC, 2803605)

- Free proprietary sync. No iCloud, Dropbox, web app, sharing or collaboration, or Sign in with Apple.
- Works offline.
- TLS in transit. AES-256 at rest. No end-to-end encryption (only "may consider").
- Calendar events are not synced (the integration is per device).
- On re-login a merge prompt appears: "Keep all to-dos (recommended)". (2803590)
- The sync protocol and conflict-resolution rules are not documented. **[unverified]**

### 7.3 Collected limits

| Limit | Value | Source |
|---|---|---|
| URL string param | 4,000 characters (unencoded) | URL |
| Notes (URL / JSON) | 10,000 characters | URL |
| Checklist items per to-do (URL / JSON) | 100 | URL |
| URL add rate | 250 items per 10 s | URL |
| Shortcuts Find results | 500 | SC |
| Shortcuts Edit/Delete warning | > 99 items | SC |
| Paste | 100 rows | OTHER |
| Mail to Things | 100 emails per 24 h; 4,000 characters per email | MAIL |
| Repeating "Start Early" (after completion, iOS) | ≤ 100 days | RN-IOS |

---

## 8. Ordering, grouping, filtering summary

| View | Order / grouping | Source |
|---|---|---|
| Today | Manual drag order. Optional group-by project/area (synced setting). This Evening section at the bottom. Calendar events at the top. | LISTS |
| Upcoming | By start date. The next 7 days individually starting tomorrow, then larger ranges (month ranges **[inferred]**). Drag to reschedule. | LISTS, AS, RN-MAC |
| Anytime | Loose to-dos first, then grouped by direct parent (sidebar order). Empty parents hidden. Limited items per group ("Next Action Steps", **[inferred]**). | LISTS |
| Someday | Someday items; "Active Projects" section for Someday to-dos inside active projects (**[inferred]**). | LISTS, RN-MAC |
| Repeating | Loose first, then by parent. By next occurrence. Paused/waiting at the bottom. | REP |
| Deadlines | Chronological, including passed deadlines. | SCHED, RN-MAC |
| Tag list (Quick Find) | All items with the tag, grouped by parent list. | 3289315 |
| Trash | Most recently deleted first (**[inferred]**). | RN-MAC |
| Inside a project | Active to-dos first. Inactive (future/Someday) items "take a back seat beneath" them, under "later items". Headings are user-ordered. Logged items and archived headings at the bottom. | LISTS, HEAD, KBD |
| Sidebar | Areas with their projects grouped beneath. User drag order. Future and Someday projects hidden. | LISTS, MOVE |

Data model hint: user ordering is a manual position per list context. Today has its own order separate from the project order. **[inferred]** The internal representation is not documented.

---

## 9. Gaps that matter for an AI-first API (derived from the sources above)

1. **No read API in the URL scheme.** `show` and `search` only drive the UI and return nothing. Reads require Shortcuts (Find Items: max 500, cannot query null deadline, parent or tags) or AppleScript (Mac only).
2. **No delete via URL.** Delete exists only in Shortcuts (iOS requires Delete Immediately) and AppleScript.
3. **No area or tag creation via URL or JSON.** Unknown tag titles are **silently dropped**. Shortcuts cannot create areas or tags either. AppleScript can (`make new area` / `tag`).
4. **Repeat rules are not automatable.** No API creates or edits rules. URL `update` refuses `when`, `deadline`, `completed`, `canceled`, `completion-date` and `duplicate` on repeating items.
5. **Headings** can be created only inside JSON project `items` (new projects only) or with Shortcuts Create Heading. JSON `update` supports only to-dos and projects. AppleScript cannot touch headings or checklists.
6. **ID return is partial:** `json` returns top-level IDs only, not nested to-dos.
7. **Status and Logged are separate states.** Completion does not imply Logged (logging interval; archived-heading exception).
8. **Inconsistent semantics across endpoints** (documented as such):
   - `anytime` is missing from `update`'s `when`.
   - `tags` is an array, but `add-tags` is a comma string; JSON `checklist-items` are objects, but the prepend/append variants are `\n` strings.
   - Project completion has three different rules (section 5).
9. **Name-based addressing** (`list`, `area`, `heading`, `show?query`) uses first-match Quick Find semantics. IDs (`list-id`, `area-id`, `heading-id`) take precedence.
10. **Natural-language dates** are accepted by the API (English only) as well as ISO `yyyy-mm-dd`. Times use `@` (`date@time`). There is no time on deadlines.
11. **Bulk safety:** the URL limits 250 items per 10 s. Shortcuts warns above 99 edits or deletes. The vendor stresses that direct DB writes corrupt data.
12. **Idempotency:** no idempotency key or client-supplied ID exists on create. **[unverified]** None is documented.

---

## 10. Explicitly unverified items

- Whether filtering by a parent tag includes child-tagged items.
- Upcoming grouping beyond 7 days (exact buckets), and how many future repeat occurrences are shown.
- Order of items within one Upcoming day.
- The exact default count for Anytime "Next Action Steps".
- Logging interval: Settings path and exact "Daily" trigger time (the setting's existence is verified via release notes).
- Trash auto-expiry (none documented).
- UI behavior when completing a project that still has open to-dos.
- Whether `update?when=` (empty) clears to Anytime; and how to detach a to-do from its parent via URL.
- Area nesting / projects inside projects: no explicit rule, only inferred "not supported".
- Things Cloud conflict-resolution semantics.
